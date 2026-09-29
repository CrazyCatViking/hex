package azurefiles

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

// The Azure Files SDK (azfile v1.7.x) signs user delegation SAS with the
// Blob Storage string-to-sign, which Azure Files rejects ("Signature did not
// match"). The key request and signature are therefore implemented here,
// following the documented Azure Files format for version 2025-07-05 and
// later:
// https://learn.microsoft.com/rest/api/storageservices/create-user-delegation-sas

const sasVersion = "2026-06-06"

// delegationKey is a user delegation key as returned by the service. The
// signed fields are kept verbatim, because the signature must repeat them
// exactly.
type delegationKey struct {
	ObjectID string `xml:"SignedOid"`
	TenantID string `xml:"SignedTid"`
	Start    string `xml:"SignedStart"`
	Expiry   string `xml:"SignedExpiry"`
	Service  string `xml:"SignedService"`
	Version  string `xml:"SignedVersion"`
	Value    string `xml:"Value"`
}

type keyInfo struct {
	XMLName xml.Name `xml:"KeyInfo"`
	Start   string   `xml:"Start"`
	Expiry  string   `xml:"Expiry"`
}

// fetchDelegationKey requests a user delegation key from the File service
// with the server's Entra credential.
func fetchDelegationKey(ctx context.Context, client *http.Client, credential azcore.TokenCredential, serviceURL string, start, expiry time.Time) (delegationKey, error) {
	token, err := credential.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{"https://storage.azure.com/.default"}})
	if err != nil {
		return delegationKey{}, fmt.Errorf("get a storage token: %w", err)
	}

	body, err := xml.Marshal(keyInfo{Start: start.UTC().Format(keyTimeFormat), Expiry: expiry.UTC().Format(keyTimeFormat)})
	if err != nil {
		return delegationKey{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, serviceURL+"?restype=service&comp=userdelegationkey", bytes.NewReader(body))
	if err != nil {
		return delegationKey{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token.Token)
	request.Header.Set("x-ms-version", sasVersion)
	request.Header.Set("x-ms-file-request-intent", "backup")
	request.Header.Set("Content-Type", "application/xml")

	response, err := client.Do(request)
	if err != nil {
		return delegationKey{}, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return delegationKey{}, err
	}
	if response.StatusCode != http.StatusOK {
		return delegationKey{}, fmt.Errorf("the File service answered HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}

	var key delegationKey
	if err := xml.Unmarshal(data, &key); err != nil {
		return delegationKey{}, fmt.Errorf("parse the user delegation key: %w", err)
	}
	if key.ObjectID == "" || key.Value == "" {
		return delegationKey{}, fmt.Errorf("the File service returned an incomplete user delegation key")
	}
	return key, nil
}

// fileStringToSign is the Azure Files user delegation string-to-sign for a
// file SAS without start time, IP range or response header overrides.
func fileStringToSign(key delegationKey, account, share, path, permissions, signedExpiry string) string {
	return strings.Join([]string{
		permissions,
		"", // signedStart
		signedExpiry,
		"/file/" + account + "/" + share + "/" + path,
		key.ObjectID,
		key.TenantID,
		key.Start,
		key.Expiry,
		key.Service,
		key.Version,
		"", // signedKeyDelegatedUserTenantId
		"", // signedDelegatedUserObjectId
		"", // signedIP
		"https",
		sasVersion,
		"", // rscc
		"", // rscd
		"", // rsce
		"", // rscl
		"", // rsct
	}, "\n")
}

// fileSAS signs a user delegation SAS for one file (sr=f) over HTTPS.
func fileSAS(key delegationKey, account, share, path, permissions string, expiry time.Time) (string, error) {
	secret, err := base64.StdEncoding.DecodeString(key.Value)
	if err != nil {
		return "", fmt.Errorf("decode the user delegation key: %w", err)
	}
	signedExpiry := expiry.UTC().Format(keyTimeFormat)
	stringToSign := fileStringToSign(key, account, share, path, permissions, signedExpiry)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(stringToSign))

	query := url.Values{
		"sv":    {sasVersion},
		"spr":   {"https"},
		"se":    {signedExpiry},
		"sr":    {"f"},
		"sp":    {permissions},
		"skoid": {key.ObjectID},
		"sktid": {key.TenantID},
		"skt":   {key.Start},
		"ske":   {key.Expiry},
		"sks":   {key.Service},
		"skv":   {key.Version},
		"sig":   {base64.StdEncoding.EncodeToString(mac.Sum(nil))},
	}
	return query.Encode(), nil
}
