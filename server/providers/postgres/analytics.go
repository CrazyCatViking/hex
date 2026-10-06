package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/jackc/pgx/v5"
)

func (d *Database) migrateAnalytics(ctx context.Context) error {
	const schema = `
		CREATE TABLE IF NOT EXISTS hex_analytics_people (
			id text PRIMARY KEY, name text NOT NULL, email text NOT NULL,
			first_seen timestamptz, last_seen timestamptz NOT NULL
		);
		CREATE TABLE IF NOT EXISTS hex_analytics_events (
			id text PRIMARY KEY, at timestamptz NOT NULL, site text NOT NULL,
			kind text NOT NULL, title text NOT NULL, actor_id text NOT NULL, actor jsonb
		);
		CREATE INDEX IF NOT EXISTS hex_analytics_events_at ON hex_analytics_events (at);
		CREATE INDEX IF NOT EXISTS hex_analytics_events_site ON hex_analytics_events (site, at);
		CREATE INDEX IF NOT EXISTS hex_analytics_events_actor ON hex_analytics_events (actor_id, at);
		CREATE TABLE IF NOT EXISTS hex_analytics_traffic (
			day date NOT NULL, site text NOT NULL, user_id text NOT NULL,
			requests bigint NOT NULL, page_views bigint NOT NULL, visits bigint NOT NULL,
			errors bigint NOT NULL, bytes bigint NOT NULL, duration_ms bigint NOT NULL,
			last_visited timestamptz, last_received timestamptz NOT NULL,
			PRIMARY KEY (day, site, user_id)
		);
		CREATE INDEX IF NOT EXISTS hex_analytics_traffic_site ON hex_analytics_traffic (site, day);
		CREATE INDEX IF NOT EXISTS hex_analytics_traffic_user ON hex_analytics_traffic (user_id, day);
		CREATE INDEX IF NOT EXISTS hex_analytics_traffic_received ON hex_analytics_traffic (last_received DESC);
		CREATE INDEX IF NOT EXISTS hex_analytics_traffic_site_received ON hex_analytics_traffic (site, last_received DESC);
		CREATE INDEX IF NOT EXISTS hex_analytics_traffic_user_received ON hex_analytics_traffic (user_id, last_received DESC);
		CREATE TABLE IF NOT EXISTS hex_analytics_receipts (id text PRIMARY KEY, at timestamptz NOT NULL);
		CREATE INDEX IF NOT EXISTS hex_analytics_receipts_at ON hex_analytics_receipts (at);
		CREATE TABLE IF NOT EXISTS hex_analytics_sessions (
			site text NOT NULL, user_id text NOT NULL, last_page_at timestamptz NOT NULL,
			PRIMARY KEY (site, user_id)
		);`
	if _, err := d.pool.Exec(ctx, schema); err != nil {
		return fmt.Errorf("migrate analytics: %w", err)
	}
	var peopleTable *string
	if err := d.pool.QueryRow(ctx, `SELECT to_regclass('hex_people')::text`).Scan(&peopleTable); err != nil {
		return err
	}
	if peopleTable != nil {
		if _, err := d.pool.Exec(ctx, `INSERT INTO hex_analytics_people (id,name,email,last_seen)
			SELECT id,name,email,last_seen FROM hex_people ON CONFLICT (id) DO NOTHING`); err != nil {
			return fmt.Errorf("import analytics people: %w", err)
		}
	}
	return nil
}

func (d *Database) ObservePerson(ctx context.Context, person hex.Person, at time.Time) error {
	const statement = `
		INSERT INTO hex_analytics_people (id, name, email, first_seen, last_seen)
		VALUES ($1, $2, $3, $4, $4)
		ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, email = EXCLUDED.email,
		last_seen = EXCLUDED.last_seen
		WHERE EXCLUDED.last_seen >= hex_analytics_people.last_seen`
	if _, err := d.pool.Exec(ctx, statement, person.ID, person.Name, person.Email, at); err != nil {
		return fmt.Errorf("observe analytics person: %w", err)
	}
	return nil
}

func (d *Database) RecordSiteEvents(ctx context.Context, events []hex.SiteEvent) error {
	batch := &pgx.Batch{}
	for _, event := range events {
		var actor []byte
		actorID := ""
		if event.Actor != nil {
			encoded, err := json.Marshal(event.Actor)
			if err != nil {
				return err
			}
			actor, actorID = encoded, event.Actor.ID
		}
		batch.Queue(`INSERT INTO hex_analytics_events (id,at,site,kind,title,actor_id,actor)
			VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (id) DO NOTHING`,
			event.ID, event.At, event.Site, event.Kind, event.Title, actorID, actor)
	}
	if err := d.pool.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("record site events: %w", err)
	}
	return nil
}

func (d *Database) RecordTraffic(ctx context.Context, events []hex.TrafficEvent) error {
	events = slices.Clone(events)
	// Every transaction acquires visitor locks in the same order.
	slices.SortFunc(events, func(a, b hex.TrafficEvent) int {
		if a.Site != b.Site {
			return strings.Compare(a.Site, b.Site)
		}
		if a.UserID != b.UserID {
			return strings.Compare(a.UserID, b.UserID)
		}
		if !a.At.Equal(b.At) {
			return a.At.Compare(b.At)
		}
		return strings.Compare(a.ID, b.ID)
	})
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackAnalytics(tx)
	cutoff := time.Now().UTC().AddDate(0, 0, -7)
	if _, err := tx.Exec(ctx, `DELETE FROM hex_analytics_receipts WHERE at < $1`, cutoff); err != nil {
		return err
	}
	for _, event := range events {
		if event.At.Before(cutoff) {
			continue
		}
		insert, err := tx.Exec(ctx, `INSERT INTO hex_analytics_receipts (id,at) VALUES ($1,$2) ON CONFLICT DO NOTHING`, event.ID, event.At)
		if err != nil {
			return err
		}
		if insert.RowsAffected() == 0 {
			continue
		}
		pageViews, visits, errors := int64(0), int64(0), int64(0)
		var lastVisited *time.Time
		if event.Status >= 400 {
			errors = 1
		}
		if event.PageView {
			pageViews, lastVisited = 1, &event.At
			if event.UserID != "" {
				// Serialize sessionization across collectors and day boundaries,
				// including a visitor whose session row does not exist yet.
				if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "hex-analytics:"+event.Site+":"+event.UserID); err != nil {
					return err
				}
				var previous *time.Time
				err := tx.QueryRow(ctx, `SELECT last_page_at FROM hex_analytics_sessions WHERE site=$1 AND user_id=$2`, event.Site, event.UserID).Scan(&previous)
				if err != nil && err != pgx.ErrNoRows {
					return err
				}
				if previous == nil || event.At.Sub(*previous) >= 30*time.Minute {
					visits = 1
				}
				if _, err := tx.Exec(ctx, `INSERT INTO hex_analytics_sessions (site,user_id,last_page_at) VALUES ($1,$2,$3)
					ON CONFLICT (site,user_id) DO UPDATE SET last_page_at=GREATEST(hex_analytics_sessions.last_page_at,EXCLUDED.last_page_at)`, event.Site, event.UserID, event.At); err != nil {
					return err
				}
			}
		}
		const statement = `INSERT INTO hex_analytics_traffic
			(day,site,user_id,requests,page_views,visits,errors,bytes,duration_ms,last_visited,last_received)
			VALUES ($1,$2,$3,1,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT (day,site,user_id) DO UPDATE SET requests=hex_analytics_traffic.requests+1,
			page_views=hex_analytics_traffic.page_views+EXCLUDED.page_views,
			visits=hex_analytics_traffic.visits+EXCLUDED.visits,
			errors=hex_analytics_traffic.errors+EXCLUDED.errors,
			bytes=hex_analytics_traffic.bytes+EXCLUDED.bytes,
			duration_ms=hex_analytics_traffic.duration_ms+EXCLUDED.duration_ms,
			last_visited=GREATEST(hex_analytics_traffic.last_visited,EXCLUDED.last_visited),
			last_received=GREATEST(hex_analytics_traffic.last_received,EXCLUDED.last_received)`
		if _, err := tx.Exec(ctx, statement, event.At.UTC().Truncate(24*time.Hour), event.Site, event.UserID,
			pageViews, visits, errors, event.Bytes, event.DurationMillis, lastVisited, event.At); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit traffic analytics: %w", err)
	}
	return nil
}

func rollbackAnalytics(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tx.Rollback(ctx); err != nil && err != pgx.ErrTxClosed {
		slog.Error("roll back analytics transaction", "error", err)
	}
}

func (d *Database) QueryAnalytics(ctx context.Context, query hex.AnalyticsQuery) (hex.AnalyticsReport, error) {
	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return hex.AnalyticsReport{}, err
	}
	defer rollbackAnalytics(tx)
	report, err := d.queryAnalyticsSummary(ctx, query, tx)
	if err != nil {
		return hex.AnalyticsReport{}, err
	}
	people := []hex.AnalyticsPerson{}
	if query.Site == "" {
		rows, err := tx.Query(ctx, `SELECT id,name,email,first_seen,last_seen FROM hex_analytics_people WHERE ($1='' OR id=$1) ORDER BY lower(name) COLLATE "C",id COLLATE "C"`, query.User)
		if err != nil {
			return hex.AnalyticsReport{}, err
		}
		for rows.Next() {
			var person hex.AnalyticsPerson
			var first *time.Time
			if err := rows.Scan(&person.ID, &person.Name, &person.Email, &first, &person.LastSeen); err != nil {
				rows.Close()
				return hex.AnalyticsReport{}, err
			}
			if first != nil {
				person.FirstSeen = first.UTC()
			}
			person.LastSeen = person.LastSeen.UTC()
			people = append(people, person)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return hex.AnalyticsReport{}, err
		}
	}
	events, err := d.analyticsEvents(ctx, query, tx)
	if err != nil {
		return hex.AnalyticsReport{}, err
	}
	report.People, report.Events = people, events
	report.Sites, err = d.analyticsRows(ctx, query, "site", tx)
	if err != nil {
		return hex.AnalyticsReport{}, err
	}
	report.Users, err = d.analyticsRows(ctx, query, "user_id", tx)
	if err != nil {
		return hex.AnalyticsReport{}, err
	}
	if err := d.labelAnalyticsVisitors(ctx, &report, tx); err != nil {
		return hex.AnalyticsReport{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return hex.AnalyticsReport{}, err
	}
	return report, nil
}

func (d *Database) labelAnalyticsVisitors(ctx context.Context, report *hex.AnalyticsReport, tx pgx.Tx) error {
	ids := []string{}
	for _, visitor := range report.Users {
		if visitor.PageViews > 0 {
			ids = append(ids, visitor.Key)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id,name,email FROM hex_analytics_people WHERE id=ANY($1)`, ids)
	if err != nil {
		return fmt.Errorf("look up analytics visitors: %w", err)
	}
	defer rows.Close()
	people := make(map[string]hex.Person)
	for rows.Next() {
		var person hex.Person
		if err := rows.Scan(&person.ID, &person.Name, &person.Email); err != nil {
			return err
		}
		people[person.ID] = person
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for index := range report.Users {
		row := &report.Users[index]
		if person, ok := people[row.Key]; ok {
			row.Person = &person
		}
	}
	return nil
}

func (d *Database) SiteTraffic(ctx context.Context, sites []string) ([]hex.AnalyticsRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sites = slices.Clone(sites)
	slices.Sort(sites)
	sites = slices.Compact(sites)
	if len(sites) == 0 {
		return []hex.AnalyticsRow{}, nil
	}
	d.trafficCacheMu.Lock()
	defer d.trafficCacheMu.Unlock()
	if d.trafficCache == nil {
		d.trafficCache = make(map[string]cachedSiteTraffic)
	}
	now := time.Now()
	missing := []string{}
	for _, site := range sites {
		if cached, ok := d.trafficCache[site]; !ok || !now.Before(cached.expires) {
			missing = append(missing, site)
		}
	}
	if len(d.trafficCache)+len(missing) > 1024 {
		clear(d.trafficCache)
		missing = slices.Clone(sites)
	}
	if len(missing) == 0 {
		return d.cachedSiteTraffic(sites), nil
	}
	const statement = `SELECT site, sum(requests)::bigint, sum(page_views)::bigint,
		sum(visits)::bigint, count(DISTINCT user_id) FILTER (WHERE page_views > 0 AND user_id <> ''),
		sum(errors)::bigint, sum(bytes)::bigint, sum(duration_ms)::bigint, max(last_visited)
		FROM hex_analytics_traffic WHERE site=ANY($1) GROUP BY site ORDER BY site`
	rows, err := d.pool.Query(ctx, statement, missing)
	if err != nil {
		return nil, fmt.Errorf("read site traffic totals: %w", err)
	}
	defer rows.Close()
	result := []hex.AnalyticsRow{}
	for rows.Next() {
		var row hex.AnalyticsRow
		var last *time.Time
		if err := rows.Scan(&row.Key, &row.Requests, &row.PageViews, &row.Visits, &row.Visitors,
			&row.Errors, &row.Bytes, &row.DurationMillis, &last); err != nil {
			return nil, err
		}
		if last != nil {
			row.LastVisited = last.UTC()
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Bound both freshness and cache size. Missing sites are cached as zero too.
	for _, site := range missing {
		d.trafficCache[site] = cachedSiteTraffic{row: hex.AnalyticsRow{Key: site}, expires: now.Add(time.Minute)}
	}
	for _, row := range result {
		d.trafficCache[row.Key] = cachedSiteTraffic{row: row, expires: now.Add(time.Minute)}
	}
	if len(d.trafficCache) > 1024 {
		// A large listing must remain correct without retaining an unbounded cache.
		result = d.cachedSiteTraffic(sites)
		clear(d.trafficCache)
		return result, nil
	}
	return d.cachedSiteTraffic(sites), nil
}

func (d *Database) cachedSiteTraffic(sites []string) []hex.AnalyticsRow {
	result := make([]hex.AnalyticsRow, 0, len(sites))
	for _, site := range sites {
		if row := d.trafficCache[site].row; row.Requests > 0 {
			result = append(result, row)
		}
	}
	slices.SortFunc(result, func(a, b hex.AnalyticsRow) int { return strings.Compare(a.Key, b.Key) })
	return result
}

func (d *Database) analyticsEvents(ctx context.Context, query hex.AnalyticsQuery, tx pgx.Tx) ([]hex.SiteEvent, error) {
	rows, err := tx.Query(ctx, `SELECT id,at,site,kind,title,actor FROM hex_analytics_events
		WHERE at >= $1 AND at < $2 AND ($3='' OR site=$3) AND ($4='' OR actor_id=$4)
		ORDER BY at DESC,id COLLATE "C" LIMIT 50`, query.From, query.Until, query.Site, query.User)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []hex.SiteEvent{}
	for rows.Next() {
		var event hex.SiteEvent
		var actor []byte
		if err := rows.Scan(&event.ID, &event.At, &event.Site, &event.Kind, &event.Title, &actor); err != nil {
			return nil, err
		}
		event.At = event.At.UTC()
		if len(actor) > 0 {
			if err := json.Unmarshal(actor, &event.Actor); err != nil {
				return nil, fmt.Errorf("decode analytics actor: %w", err)
			}
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

const analyticsTrafficColumns = `COALESCE(sum(requests),0)::bigint AS requests, COALESCE(sum(page_views),0)::bigint AS page_views,
	COALESCE(sum(visits),0)::bigint AS visits, count(DISTINCT user_id) FILTER (WHERE page_views>0 AND user_id<>'') AS visitors,
	COALESCE(sum(errors),0)::bigint AS errors, COALESCE(sum(bytes),0)::bigint AS bytes,
	COALESCE(sum(duration_ms),0)::bigint AS duration_ms, max(last_visited) AS last_visited`

const analyticsTrafficFilter = ` FROM hex_analytics_traffic WHERE day >= $1 AND day < $2
	AND ($3='' OR site=$3) AND ($4='' OR user_id=$4)`

func (d *Database) analyticsRows(ctx context.Context, query hex.AnalyticsQuery, group string, tx pgx.Tx) ([]hex.AnalyticsRow, error) {
	// group is an internal constant, never caller input.
	statement := `SELECT ` + group + `,` + analyticsTrafficColumns + analyticsTrafficFilter
	if group == "user_id" {
		statement += ` AND user_id<>''`
	}
	statement += ` GROUP BY ` + group + ` ORDER BY sum(page_views) DESC,` + group + ` COLLATE "C"`
	rows, err := tx.Query(ctx, statement, query.From, query.Until, query.Site, query.User)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []hex.AnalyticsRow
	for rows.Next() {
		var row hex.AnalyticsRow
		var last *time.Time
		if err := rows.Scan(&row.Key, &row.Requests, &row.PageViews, &row.Visits, &row.Visitors, &row.Errors, &row.Bytes, &row.DurationMillis, &last); err != nil {
			return nil, err
		}
		if last != nil {
			row.LastVisited = last.UTC()
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (d *Database) QueryAnalyticsSummary(ctx context.Context, query hex.AnalyticsQuery) (hex.AnalyticsReport, error) {
	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return hex.AnalyticsReport{}, err
	}
	defer rollbackAnalytics(tx)
	report, err := d.queryAnalyticsSummary(ctx, query, tx)
	if err != nil {
		return hex.AnalyticsReport{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return hex.AnalyticsReport{}, err
	}
	return report, nil
}

func (d *Database) queryAnalyticsSummary(ctx context.Context, query hex.AnalyticsQuery, tx pgx.Tx) (hex.AnalyticsReport, error) {
	report := hex.SummarizeAnalyticsSummary(query, nil, nil, nil, time.Time{})
	var last *time.Time
	traffic := &report.Traffic
	if err := tx.QueryRow(ctx, `SELECT `+analyticsTrafficColumns+analyticsTrafficFilter,
		query.From, query.Until, query.Site, query.User).Scan(&traffic.Requests, &traffic.PageViews, &traffic.Visits, &traffic.Visitors, &traffic.Errors, &traffic.Bytes, &traffic.DurationMillis, &last); err != nil {
		return report, err
	}
	if last != nil {
		traffic.LastVisited = last.UTC()
	}
	if err := tx.QueryRow(ctx, `SELECT max(last_received) FROM hex_analytics_traffic WHERE ($1='' OR site=$1) AND ($2='' OR user_id=$2)`, query.Site, query.User).Scan(&last); err != nil {
		return report, err
	}
	if last != nil {
		report.LastCollected = last.UTC()
	}
	days := make(map[string]*hex.AnalyticsDay, len(report.Days))
	for i := range report.Days {
		days[report.Days[i].Date] = &report.Days[i]
	}
	rows, err := tx.Query(ctx, `SELECT day,`+analyticsTrafficColumns+analyticsTrafficFilter+` GROUP BY day`, query.From, query.Until, query.Site, query.User)
	if err != nil {
		return report, err
	}
	for rows.Next() {
		var at time.Time
		var total hex.TrafficTotals
		if err := rows.Scan(&at, &total.Requests, &total.PageViews, &total.Visits, &total.Visitors, &total.Errors, &total.Bytes, &total.DurationMillis, &last); err != nil {
			rows.Close()
			return report, err
		}
		if last != nil {
			total.LastVisited = last.UTC()
		}
		if day := days[at.Format(time.DateOnly)]; day != nil {
			day.TrafficTotals = total
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return report, err
	}
	rows, err = tx.Query(ctx, `SELECT (at AT TIME ZONE 'UTC')::date,kind,count(*) FROM hex_analytics_events
		WHERE at >= $1 AND at < $2 AND ($3='' OR site=$3) AND ($4='' OR actor_id=$4) GROUP BY 1,kind`, query.From, query.Until, query.Site, query.User)
	if err != nil {
		return report, err
	}
	for rows.Next() {
		var at time.Time
		var kind string
		var count int
		if err := rows.Scan(&at, &kind, &count); err != nil {
			rows.Close()
			return report, err
		}
		day := days[at.Format(time.DateOnly)]
		if day == nil {
			continue
		}
		switch kind {
		case "created":
			report.Created += count
			day.Created = count
		case "published":
			report.Publications += count
			day.Publications = count
		case "unpublished":
			report.Unpublished += count
			day.Unpublished = count
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return report, err
	}
	if query.Site != "" {
		return report, nil
	}
	now := time.Now().UTC()
	if err := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE last_seen >= $1),
		count(*) FILTER (WHERE last_seen >= $2) FROM hex_analytics_people WHERE ($3='' OR id=$3)`, now.AddDate(0, 0, -7), now.AddDate(0, 0, -30), query.User).Scan(&report.KnownUsers, &report.Active7Days, &report.Active30Days); err != nil {
		return report, err
	}
	rows, err = tx.Query(ctx, `SELECT (first_seen AT TIME ZONE 'UTC')::date,count(*) FROM hex_analytics_people
		WHERE first_seen >= $1 AND first_seen < $2 AND ($3='' OR id=$3) GROUP BY 1`, query.From, query.Until, query.User)
	if err != nil {
		return report, err
	}
	defer rows.Close()
	for rows.Next() {
		var at time.Time
		var count int
		if err := rows.Scan(&at, &count); err != nil {
			return report, err
		}
		if day := days[at.Format(time.DateOnly)]; day != nil {
			day.NewUsers = count
			report.NewUsers += count
		}
	}
	return report, rows.Err()
}

func (d *Database) QueryAnalyticsVisitors(ctx context.Context, query hex.AnalyticsVisitorsQuery) (hex.AnalyticsVisitorsPage, error) {
	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return hex.AnalyticsVisitorsPage{}, err
	}
	defer rollbackAnalytics(tx)
	// Aggregate before joining identities, searching or selecting a page. API-only
	// users do not enter this table; anonymous totals and peak stay unfiltered.
	const visitors = `WITH visitors AS (
		SELECT user_id,` + analyticsTrafficColumns + analyticsTrafficFilter + ` GROUP BY user_id HAVING sum(page_views)>0
	), named AS (
		SELECT v.*,p.name,p.email,COALESCE(NULLIF(p.name,''),NULLIF(p.email,''),v.user_id) AS label
		FROM visitors v LEFT JOIN hex_analytics_people p ON p.id=v.user_id WHERE v.user_id<>''
	), filtered AS (
		SELECT * FROM named WHERE $5='' OR strpos(lower(label||' '||COALESCE(email,'')||' '||user_id),lower($5))>0
	) `
	args := []any{query.From, query.Until, query.Site, query.User, query.Search}
	page := hex.AnalyticsVisitorsPage{}
	if err := tx.QueryRow(ctx, visitors+`SELECT (SELECT count(*) FROM filtered),
		COALESCE((SELECT page_views FROM visitors WHERE user_id=''),0),COALESCE((SELECT max(page_views) FROM named),0)`, args...).Scan(&page.Total, &page.AnonymousPageViews, &page.PeakPageViews); err != nil {
		return page, err
	}
	limit := max(1, min(query.Limit, 100))
	page.Page = min(max(1, query.Page), max(1, (page.Total+limit-1)/limit))
	order := "page_views DESC,"
	switch query.Sort {
	case "visits":
		order = "visits DESC,"
	case "recent":
		order = "last_visited DESC NULLS LAST,"
	case "name":
		order = ""
	}
	args = append(args, limit, (page.Page-1)*limit)
	rows, err := tx.Query(ctx, visitors+`SELECT user_id,requests,page_views,visits,visitors,errors,bytes,duration_ms,last_visited,name,email
		FROM filtered ORDER BY `+order+`lower(label) COLLATE "C",user_id COLLATE "C" LIMIT $6 OFFSET $7`, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var row hex.AnalyticsRow
		var last *time.Time
		var name, email *string
		if err := rows.Scan(&row.Key, &row.Requests, &row.PageViews, &row.Visits, &row.Visitors, &row.Errors, &row.Bytes, &row.DurationMillis, &last, &name, &email); err != nil {
			return page, err
		}
		if last != nil {
			row.LastVisited = last.UTC()
		}
		if name != nil && email != nil {
			row.Person = &hex.Person{ID: row.Key, Name: *name, Email: *email}
		}
		page.Rows = append(page.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if err := tx.Commit(ctx); err != nil {
		return page, err
	}
	return page, nil
}
