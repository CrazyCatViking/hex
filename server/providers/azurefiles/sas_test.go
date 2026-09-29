package azurefiles

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"testing"
	"time"
)

// The string Azure Files reported as the one it signs, from an
// AuthenticationFailed response to an upload with a Blob-format signature.
const serviceStringToSign = "cw\n" +
	"\n" +
	"2026-09-29T19:04:23Z\n" +
	"/file/hexsites6a840e574ec8/sites/public/sites/test-project/assets/index-D55i6mck.css\n" +
	"11a1aac5-5c7d-47ab-af6f-fa423a6f77f4\n" +
	"c166b9c4-5053-4eec-9665-aba0782d0804\n" +
	"2026-09-29T17:58:25Z\n" +
	"2026-09-30T06:03:25Z\n" +
	"f\n" +
	"2026-06-06\n" +
	"\n" +
	"\n" +
	"\n" +
	"https\n" +
	"2026-06-06\n" +
	"\n" +
	"\n" +
	"\n" +
	"\n"

var testKey = delegationKey{
	ObjectID: "11a1aac5-5c7d-47ab-af6f-fa423a6f77f4",
	TenantID: "c166b9c4-5053-4eec-9665-aba0782d0804",
	Start:    "2026-09-29T17:58:25Z",
	Expiry:   "2026-09-30T06:03:25Z",
	Service:  "f",
	Version:  "2026-06-06",
	Value:    base64.StdEncoding.EncodeToString([]byte("fixture delegation key")),
}

func TestStringToSignMatchesAzureFiles(t *testing.T) {
	got := fileStringToSign(testKey, "hexsites6a840e574ec8", "sites", "public/sites/test-project/assets/index-D55i6mck.css", "cw", "2026-09-29T19:04:23Z")
	if got != serviceStringToSign {
		t.Fatalf("string-to-sign differs from what Azure Files signs:\n%q\nwant\n%q", got, serviceStringToSign)
	}
}

func TestFileSASCarriesTheSignedFields(t *testing.T) {
	expiry := time.Date(2026, 9, 29, 19, 4, 23, 0, time.UTC)
	encoded, err := fileSAS(testKey, "hexsites6a840e574ec8", "sites", "public/sites/test-project/assets/index-D55i6mck.css", "cw", expiry)
	if err != nil {
		t.Fatal(err)
	}
	query, err := url.ParseQuery(encoded)
	if err != nil {
		t.Fatal(err)
	}

	mac := hmac.New(sha256.New, []byte("fixture delegation key"))
	mac.Write([]byte(serviceStringToSign))
	want := map[string]string{
		"sv": "2026-06-06", "spr": "https", "se": "2026-09-29T19:04:23Z", "sr": "f", "sp": "cw",
		"skoid": testKey.ObjectID, "sktid": testKey.TenantID, "skt": testKey.Start, "ske": testKey.Expiry,
		"sks": "f", "skv": "2026-06-06",
		"sig": base64.StdEncoding.EncodeToString(mac.Sum(nil)),
	}
	for name, value := range want {
		if query.Get(name) != value {
			t.Errorf("%s = %q, want %q", name, query.Get(name), value)
		}
	}
	if len(query) != len(want) {
		t.Errorf("unexpected SAS parameters: %v", query)
	}
}
