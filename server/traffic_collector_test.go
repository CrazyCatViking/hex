package hex

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTrafficLogClassificationAndValidation(t *testing.T) {
	now := time.Now().UTC()
	base := nginxTrafficLog{
		Version: 1, ID: strings.Repeat("a", 32), Time: strconv.FormatFloat(float64(now.UnixMilli())/1000, 'f', 3, 64),
		Site: "demo", User: base64.RawURLEncoding.EncodeToString([]byte("verified-user")), Method: "GET", Status: "200",
		Bytes: "123", Duration: "0.012", ContentType: "text/html", API: "0",
	}
	tests := []struct {
		name     string
		change   func(*nginxTrafficLog)
		pageView bool
		invalid  bool
	}{
		{"document", func(l *nginxTrafficLog) { l.Destination = "document" }, true, false},
		{"HTML without fetch metadata", func(l *nginxTrafficLog) {}, true, false},
		{"asset", func(l *nginxTrafficLog) { l.ContentType = "text/css" }, false, false},
		{"API HTML", func(l *nginxTrafficLog) { l.API = "1" }, false, false},
		{"HEAD", func(l *nginxTrafficLog) { l.Method = "HEAD" }, false, false},
		{"forbidden document", func(l *nginxTrafficLog) { l.Status = "403"; l.Destination = "document" }, false, false},
		{"missing document", func(l *nginxTrafficLog) { l.Status = "404" }, false, false},
		{"conditional document", func(l *nginxTrafficLog) { l.Status = "304"; l.Destination = "document"; l.ContentType = "" }, true, false},
		{"redirect", func(l *nginxTrafficLog) { l.Status = "301" }, false, false},
		{"bad visitor", func(l *nginxTrafficLog) { l.User = "!!!" }, false, true},
		{"path as site", func(l *nginxTrafficLog) { l.Site = "../demo" }, false, true},
		{"bad request ID", func(l *nginxTrafficLog) { l.ID = strings.Repeat("z", 32) }, false, true},
		{"negative bytes", func(l *nginxTrafficLog) { l.Bytes = "-1" }, false, true},
		{"NaN duration", func(l *nginxTrafficLog) { l.Duration = "NaN" }, false, true},
		{"infinite time", func(l *nginxTrafficLog) { l.Time = "Inf" }, false, true},
		{"old receipt", func(l *nginxTrafficLog) { l.Time = strconv.FormatInt(now.AddDate(0, 0, -8).Unix(), 10) }, false, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			log := base
			test.change(&log)
			data, err := json.Marshal(log)
			if err != nil {
				t.Fatal(err)
			}
			event, err := parseTrafficLog(append([]byte("<190>Oct 3 12:00:00 hex_traffic: "), data...), now)
			if (err != nil) != test.invalid {
				t.Fatalf("unexpected validation: %v", err)
			}
			if err == nil && (event.PageView != test.pageView || event.UserID != "verified-user" || event.DurationMillis != 12) {
				t.Fatalf("wrong traffic classification: %+v", event)
			}
		})
	}
}
