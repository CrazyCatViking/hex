package postgres

import (
	"context"
	"strings"
	"time"

	hex "github.com/crazycatviking/hex/server"
	"github.com/jackc/pgx/v5"
)

func (d *Database) QueryAdminAnalytics(ctx context.Context, query hex.AdminAnalyticsQuery) (hex.AdminAnalyticsResult, error) {
	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return hex.AdminAnalyticsResult{}, err
	}
	defer rollbackAnalytics(tx)
	result := hex.AdminAnalyticsResult{AnalyticsReport: hex.AnalyticsReport{Query: query.AnalyticsQuery}}
	if query.Section == "users" {
		err = d.adminPeoplePage(ctx, tx, query, &result)
	} else {
		result.AnalyticsReport, err = d.queryAnalyticsSummary(ctx, query.AnalyticsQuery, tx)
		if err == nil {
			result.Events, err = d.analyticsEvents(ctx, query.AnalyticsQuery, tx)
		}
		if err == nil && (query.Section == "overview" || query.Section == "user") {
			limit, offset := 5, 0
			if query.Section == "user" {
				limit = max(1, min(query.Limit, 25))
				offset = (max(1, query.Page) - 1) * limit
			}
			result.Sites, result.Total, err = d.adminTrafficRows(ctx, tx, query.AnalyticsQuery, "site", limit, offset)
			result.Page = min(max(1, query.Page), max(1, (result.Total+limit-1)/limit))
		}
		if err == nil && query.Section == "overview" {
			result.Users, _, err = d.adminTrafficRows(ctx, tx, query.AnalyticsQuery, "user_id", 5, 0)
			if err == nil {
				err = d.labelAnalyticsVisitors(ctx, &result.AnalyticsReport, tx)
			}
			for _, row := range result.Users {
				if row.Person != nil {
					result.People = append(result.People, hex.AnalyticsPerson{Person: *row.Person})
				}
			}
		}
		if err == nil && query.Section == "user" {
			personQuery := query
			personQuery.Page, personQuery.Limit = 1, 1
			personResult := hex.AdminAnalyticsResult{}
			err = d.adminPeoplePage(ctx, tx, personQuery, &personResult)
			result.People = personResult.People
		}
	}
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func (d *Database) adminTrafficRows(ctx context.Context, tx pgx.Tx, query hex.AnalyticsQuery, group string, limit, offset int) ([]hex.AnalyticsRow, int, error) {
	filter := analyticsTrafficFilter
	if group == "user_id" {
		filter += ` AND user_id<>''`
	}
	total := 0
	if err := tx.QueryRow(ctx, `SELECT count(DISTINCT `+group+`)`+filter, query.From, query.Until, query.Site, query.User).Scan(&total); err != nil {
		return nil, 0, err
	}
	offset = min(offset, (max(1, (total+limit-1)/limit)-1)*limit)
	rows, err := tx.Query(ctx, `SELECT `+group+`,`+analyticsTrafficColumns+filter+` GROUP BY `+group+` ORDER BY page_views DESC,`+group+` COLLATE "C" LIMIT $5 OFFSET $6`, query.From, query.Until, query.Site, query.User, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := []hex.AnalyticsRow{}
	for rows.Next() {
		var row hex.AnalyticsRow
		var last *time.Time
		if err := rows.Scan(&row.Key, &row.Requests, &row.PageViews, &row.Visits, &row.Visitors, &row.Errors, &row.Bytes, &row.DurationMillis, &last); err != nil {
			return nil, 0, err
		}
		if last != nil {
			row.LastVisited = *last
		}
		result = append(result, row)
	}
	return result, total, rows.Err()
}

func (d *Database) adminPeoplePage(ctx context.Context, tx pgx.Tx, query hex.AdminAnalyticsQuery, result *hex.AdminAnalyticsResult) error {
	const base = `WITH traffic AS (SELECT user_id,COALESCE(sum(page_views),0)::bigint AS views,COALESCE(sum(visits),0)::bigint AS visits FROM hex_analytics_traffic WHERE day >= $1 AND day < $2 AND user_id<>'' AND ($3='' OR user_id=$3) GROUP BY user_id), matched AS (
		SELECT p.*,COALESCE(t.views,0) AS views,COALESCE(t.visits,0) AS visits FROM hex_analytics_people p LEFT JOIN traffic t ON t.user_id=p.id
		WHERE ($3='' OR p.id=$3) AND strpos(lower(p.name||' '||p.email||' '||p.id),$4)>0)`
	args := []any{query.From, query.Until, query.User, strings.ToLower(query.Search)}
	if err := tx.QueryRow(ctx, base+` SELECT count(*) FROM matched`, args...).Scan(&result.Total); err != nil {
		return err
	}
	order := `views DESC,id COLLATE "C"`
	if query.Sort == "name" {
		order = `lower(COALESCE(NULLIF(name,''),NULLIF(email,''),id)) COLLATE "C",id COLLATE "C"`
	}
	if query.Sort == "recent" {
		order = `last_seen DESC,id COLLATE "C"`
	}
	limit := max(1, min(query.Limit, 25))
	result.Page = min(max(1, query.Page), max(1, (result.Total+limit-1)/limit))
	args = append(args, limit, (result.Page-1)*limit)
	rows, err := tx.Query(ctx, base+` SELECT id,name,email,first_seen,last_seen,views,visits FROM matched ORDER BY `+order+` LIMIT $5 OFFSET $6`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var person hex.AnalyticsPerson
		var first *time.Time
		var row hex.AnalyticsRow
		if err := rows.Scan(&person.ID, &person.Name, &person.Email, &first, &person.LastSeen, &row.PageViews, &row.Visits); err != nil {
			return err
		}
		if first != nil {
			person.FirstSeen = *first
		}
		row.Key = person.ID
		result.People = append(result.People, person)
		result.Users = append(result.Users, row)
	}
	return rows.Err()
}
