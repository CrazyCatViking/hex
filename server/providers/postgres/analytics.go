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
		INSERT INTO hex_analytics_people (id, name, email, last_seen)
		SELECT id, name, email, last_seen FROM hex_people ON CONFLICT (id) DO NOTHING;
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
		CREATE TABLE IF NOT EXISTS hex_analytics_receipts (id text PRIMARY KEY, at timestamptz NOT NULL);
		CREATE INDEX IF NOT EXISTS hex_analytics_receipts_at ON hex_analytics_receipts (at);
		CREATE TABLE IF NOT EXISTS hex_analytics_sessions (
			site text NOT NULL, user_id text NOT NULL, last_page_at timestamptz NOT NULL,
			PRIMARY KEY (site, user_id)
		);`
	if _, err := d.pool.Exec(ctx, schema); err != nil {
		return fmt.Errorf("migrate analytics: %w", err)
	}
	return nil
}

func (d *Database) ObservePerson(ctx context.Context, person hex.Person, at time.Time) error {
	const statement = `
		INSERT INTO hex_analytics_people (id, name, email, first_seen, last_seen)
		VALUES ($1, $2, $3, $4, $4)
		ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, email = EXCLUDED.email,
		last_seen = GREATEST(hex_analytics_people.last_seen, EXCLUDED.last_seen)`
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
	people := []hex.AnalyticsPerson{}
	if query.Site == "" {
		rows, err := d.pool.Query(ctx, `SELECT id,name,email,first_seen,last_seen FROM hex_analytics_people WHERE ($1='' OR id=$1) ORDER BY lower(name),id`, query.User)
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
				person.FirstSeen = *first
			}
			people = append(people, person)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return hex.AnalyticsReport{}, err
		}
	}
	events, err := d.analyticsEvents(ctx, query)
	if err != nil {
		return hex.AnalyticsReport{}, err
	}
	buckets, err := d.analyticsBuckets(ctx, query)
	if err != nil {
		return hex.AnalyticsReport{}, err
	}
	var last *time.Time
	if err := d.pool.QueryRow(ctx, `SELECT max(last_received) FROM hex_analytics_traffic WHERE ($1='' OR site=$1) AND ($2='' OR user_id=$2)`, query.Site, query.User).Scan(&last); err != nil {
		return hex.AnalyticsReport{}, err
	}
	lastCollected := time.Time{}
	if last != nil {
		lastCollected = *last
	}
	report := hex.SummarizeAnalytics(query, people, events, buckets, lastCollected)
	if err := d.labelAnalyticsVisitors(ctx, &report); err != nil {
		return hex.AnalyticsReport{}, err
	}
	return report, nil
}

func (d *Database) labelAnalyticsVisitors(ctx context.Context, report *hex.AnalyticsReport) error {
	ids := []string{}
	for _, visitor := range report.Users {
		if visitor.PageViews > 0 {
			ids = append(ids, visitor.Key)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := d.pool.Query(ctx, `SELECT id,name,email FROM hex_analytics_people WHERE id=ANY($1)`, ids)
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
	if len(sites) == 0 {
		return []hex.AnalyticsRow{}, nil
	}
	const statement = `SELECT site, sum(requests)::bigint, sum(page_views)::bigint,
		sum(visits)::bigint, count(DISTINCT user_id) FILTER (WHERE page_views > 0 AND user_id <> ''),
		sum(errors)::bigint, sum(bytes)::bigint, sum(duration_ms)::bigint, max(last_visited)
		FROM hex_analytics_traffic WHERE site=ANY($1) GROUP BY site ORDER BY site`
	rows, err := d.pool.Query(ctx, statement, sites)
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
			row.LastVisited = *last
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (d *Database) analyticsEvents(ctx context.Context, query hex.AnalyticsQuery) ([]hex.SiteEvent, error) {
	rows, err := d.pool.Query(ctx, `SELECT id,at,site,kind,title,actor FROM hex_analytics_events
		WHERE at >= $1 AND at < $2 AND ($3='' OR site=$3) AND ($4='' OR actor_id=$4)`, query.From, query.Until, query.Site, query.User)
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
		if len(actor) > 0 {
			if err := json.Unmarshal(actor, &event.Actor); err != nil {
				return nil, fmt.Errorf("decode analytics actor: %w", err)
			}
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (d *Database) analyticsBuckets(ctx context.Context, query hex.AnalyticsQuery) ([]hex.TrafficBucket, error) {
	rows, err := d.pool.Query(ctx, `SELECT day,site,user_id,requests,page_views,visits,errors,bytes,duration_ms,last_visited FROM hex_analytics_traffic
		WHERE day >= $1 AND day < $2 AND ($3='' OR site=$3) AND ($4='' OR user_id=$4)`, query.From, query.Until, query.Site, query.User)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	buckets := []hex.TrafficBucket{}
	for rows.Next() {
		var bucket hex.TrafficBucket
		var last *time.Time
		if err := rows.Scan(&bucket.Day, &bucket.Site, &bucket.UserID, &bucket.Requests, &bucket.PageViews, &bucket.Visits,
			&bucket.Errors, &bucket.Bytes, &bucket.DurationMillis, &last); err != nil {
			return nil, err
		}
		if last != nil {
			bucket.LastVisited = *last
		}
		buckets = append(buckets, bucket)
	}
	return buckets, rows.Err()
}
