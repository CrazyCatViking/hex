package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	hex "github.com/crazycatviking/hex/server"
	"github.com/jackc/pgx/v5"
)

func (d *Database) Mutate(ctx context.Context, site, collection, id, creator string, mutation hex.DocumentMutation) (hex.Document, error) {
	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return hex.Document{}, fmt.Errorf("begin guarded document mutation: %w", err)
	}
	defer func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			slog.Error("roll back document mutation", "error", err)
		}
	}()
	// The advisory lock also covers missing IDs. Put and Delete participate in
	// this protocol, so callers cannot race an insert past create authorization.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "hex-document:"+site+":"+collection+":"+id); err != nil {
		return hex.Document{}, fmt.Errorf("lock document mutation: %w", err)
	}
	document := hex.Document{ID: id}
	err = tx.QueryRow(ctx, `SELECT data, created_by FROM hex_documents WHERE site=$1 AND collection=$2 AND id=$3 FOR UPDATE`, site, collection, id).Scan(&document.Data, &document.CreatedBy)
	var current *hex.Document
	if err == nil {
		copy := document
		current = &copy
	} else if errors.Is(err, pgx.ErrNoRows) {
		document.CreatedBy = creator
	} else {
		return hex.Document{}, fmt.Errorf("read document for mutation: %w", err)
	}
	data, err := mutation(current)
	if err != nil {
		return hex.Document{}, err
	}
	if data == nil {
		_, err = tx.Exec(ctx, `DELETE FROM hex_documents WHERE site=$1 AND collection=$2 AND id=$3`, site, collection, id)
	} else {
		if err := hex.ValidateDocumentData(data); err != nil {
			return hex.Document{}, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO hex_documents(site,collection,id,data,created_by) VALUES($1,$2,$3,$4,$5) ON CONFLICT(site,collection,id) DO UPDATE SET data=EXCLUDED.data`, site, collection, id, data, document.CreatedBy)
	}
	if err != nil {
		return hex.Document{}, fmt.Errorf("mutate document: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return hex.Document{}, fmt.Errorf("commit document mutation: %w", err)
	}
	document.Data = data
	return document, nil
}

// conditionSQL binds both JSON values and field paths; neither is SQL source.
func conditionSQL(condition *hex.RecordCondition, arguments *[]any) (string, error) {
	if condition == nil {
		return "TRUE", nil
	}
	bind := func(value any) string {
		*arguments = append(*arguments, value)
		return fmt.Sprintf("$%d", len(*arguments))
	}
	if condition.All != nil || condition.Any != nil {
		children, join, empty := condition.All, " AND ", "TRUE"
		if condition.Any != nil {
			children, join, empty = condition.Any, " OR ", "FALSE"
		}
		var expressions []string
		for _, child := range children {
			expression, err := conditionSQL(&child, arguments)
			if err != nil {
				return "", err
			}
			expressions = append(expressions, expression)
		}
		if len(expressions) == 0 {
			return empty, nil
		}
		return "(" + strings.Join(expressions, join) + ")", nil
	}
	var field string
	switch condition.Field {
	case "createdBy":
		field = "to_jsonb(NULLIF(created_by, ''))"
	case "id":
		field = "to_jsonb(id)"
	default:
		if !strings.HasPrefix(condition.Field, "data.") {
			return "", errors.New("invalid authorization field")
		}
		field = "(data #> " + bind(strings.Split(strings.TrimPrefix(condition.Field, "data."), ".")) + "::text[])"
	}
	value := bind(string(condition.Value)) + "::jsonb"
	switch condition.Op {
	case "eq":
		return "COALESCE(" + field + " = " + value + ", FALSE)", nil
	case "ne":
		return "COALESCE(" + field + " <> " + value + ", FALSE)", nil
	case "in":
		return "EXISTS (SELECT 1 FROM jsonb_array_elements(" + value + ") candidate WHERE candidate = " + field + ")", nil
	case "contains":
		return "EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(" + field + ") = 'array' THEN " + field + " ELSE '[]'::jsonb END) candidate WHERE candidate = " + value + ")", nil
	default:
		return "", errors.New("invalid authorization comparison")
	}
}

func (d *Database) ListMatching(ctx context.Context, site, collection string, options hex.ListOptions, filter *hex.RecordCondition) ([]hex.Document, error) {
	arguments := []any{site, collection, options.After, options.Limit, options.CreatedBy, options.MaxDocumentBytes}
	expression, err := conditionSQL(filter, &arguments)
	if err != nil {
		return nil, err
	}
	query := `SELECT id, CASE WHEN $6 = 0 OR octet_length(data::text) <= $6 THEN data ELSE NULL END, created_by
		FROM hex_documents WHERE site=$1 AND collection=$2 AND id>$3 AND ($5='' OR created_by=$5)
		AND (` + expression + `) ORDER BY id LIMIT $4`
	rows, err := d.pool.Query(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list authorized documents: %w", err)
	}
	defer rows.Close()
	documents := []hex.Document{}
	for rows.Next() {
		var document hex.Document
		if err := rows.Scan(&document.ID, &document.Data, &document.CreatedBy); err != nil {
			return nil, err
		}
		if document.Data == nil {
			return nil, hex.ErrDocumentReadLimit
		}
		documents = append(documents, document)
	}
	return documents, rows.Err()
}
