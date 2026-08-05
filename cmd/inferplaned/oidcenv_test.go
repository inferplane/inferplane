package main

import (
	"testing"
)

func getenvMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadOIDCEnvAbsentIsNil(t *testing.T) {
	o, err := loadOIDCEnv(getenvMap(nil))
	if err != nil || o != nil {
		t.Fatalf("all-unset must be (nil, nil), got (%+v, %v)", o, err)
	}
}

func TestLoadOIDCEnvValid(t *testing.T) {
	o, err := loadOIDCEnv(getenvMap(map[string]string{
		"INFERPLANED_OIDC_ISSUER":         "https://cognito-idp.ap-northeast-2.amazonaws.com/pool-1",
		"INFERPLANED_OIDC_CLIENT_ID":      "client-1",
		"INFERPLANED_OIDC_GROUPS_CLAIM":   "cognito:groups",
		"INFERPLANED_OIDC_ALLOWED_GROUPS": " ops , finance ,,",
		"INFERPLANED_OIDC_LOGIN_ORIGINS":  "https://console.example.com, https://console.example.com/ ",
	}))
	if err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if o.Issuer != "https://cognito-idp.ap-northeast-2.amazonaws.com/pool-1" || o.ClientID != "client-1" {
		t.Fatalf("issuer/client_id not parsed: %+v", o)
	}
	if o.GroupsClaim != "cognito:groups" {
		t.Fatalf("groups_claim not honored: %q", o.GroupsClaim)
	}
	if len(o.AllowedGroups) != 2 || o.AllowedGroups[0] != "ops" || o.AllowedGroups[1] != "finance" {
		t.Fatalf("allowed_groups not trimmed/deduped-empty: %v", o.AllowedGroups)
	}
	// "https://console.example.com" and ".../" normalize to the same origin — dup rejected below;
	// this case uses distinct-looking values so both survive if the comparison were naive.
	if len(o.LoginOrigins) != 2 {
		t.Fatalf("login_origins not split/trimmed: %v", o.LoginOrigins)
	}
}

func TestLoadOIDCEnvGroupsClaimDefault(t *testing.T) {
	o, err := loadOIDCEnv(getenvMap(map[string]string{
		"INFERPLANED_OIDC_ISSUER":         "https://idp.example.com",
		"INFERPLANED_OIDC_CLIENT_ID":      "client-1",
		"INFERPLANED_OIDC_ALLOWED_GROUPS": "ops",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if o.GroupsClaim != "groups" {
		t.Fatalf("default groups_claim = %q, want %q", o.GroupsClaim, "groups")
	}
}

func TestLoadOIDCEnvRejections(t *testing.T) {
	base := func() map[string]string {
		return map[string]string{
			"INFERPLANED_OIDC_ISSUER":         "https://idp.example.com",
			"INFERPLANED_OIDC_CLIENT_ID":      "client-1",
			"INFERPLANED_OIDC_ALLOWED_GROUPS": "ops",
		}
	}
	cases := map[string]struct {
		mutate  func(map[string]string)
		wantSub string
	}{
		"issuer without client_id": {func(m map[string]string) { delete(m, "INFERPLANED_OIDC_CLIENT_ID") }, "client_id"},
		"client_id without issuer": {func(m map[string]string) { delete(m, "INFERPLANED_OIDC_ISSUER") }, "issuer"},
		"issuer not https":         {func(m map[string]string) { m["INFERPLANED_OIDC_ISSUER"] = "http://idp.example.com" }, "https"},
		"issuer has query":         {func(m map[string]string) { m["INFERPLANED_OIDC_ISSUER"] = "https://idp.example.com?x=1" }, "issuer"},
		"issuer has userinfo":      {func(m map[string]string) { m["INFERPLANED_OIDC_ISSUER"] = "https://u:p@idp.example.com" }, "issuer"},
		"login origin not https":   {func(m map[string]string) { m["INFERPLANED_OIDC_LOGIN_ORIGINS"] = "http://console.example.com" }, "https"},
		"login origin has path":    {func(m map[string]string) { m["INFERPLANED_OIDC_LOGIN_ORIGINS"] = "https://console.example.com/x" }, "path"},
		"login origin duplicate": {func(m map[string]string) {
			m["INFERPLANED_OIDC_LOGIN_ORIGINS"] = "https://console.example.com,https://console.example.com"
		}, "duplicate"},
		"login origins without issuer": {func(m map[string]string) {
			delete(m, "INFERPLANED_OIDC_ISSUER")
			delete(m, "INFERPLANED_OIDC_CLIENT_ID")
			m["INFERPLANED_OIDC_LOGIN_ORIGINS"] = "https://console.example.com"
		}, "issuer"},
		"no allowed groups": {func(m map[string]string) { delete(m, "INFERPLANED_OIDC_ALLOWED_GROUPS") }, "allowed_groups"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := base()
			tc.mutate(m)
			_, err := loadOIDCEnv(getenvMap(m))
			if err == nil {
				t.Fatalf("%s: accepted", name)
			}
		})
	}
}

func TestOIDCEnvMapping(t *testing.T) {
	o, err := loadOIDCEnv(getenvMap(map[string]string{
		"INFERPLANED_OIDC_ISSUER":         "https://idp.example.com",
		"INFERPLANED_OIDC_CLIENT_ID":      "client-1",
		"INFERPLANED_OIDC_ALLOWED_GROUPS": "ops,finance",
	}))
	if err != nil {
		t.Fatal(err)
	}
	m := o.mapping()
	if len(m.AdminGroups) != 2 {
		t.Fatalf("mapping.AdminGroups = %v", m.AdminGroups)
	}
}

func TestOIDCEnvConnectSrc(t *testing.T) {
	o, err := loadOIDCEnv(getenvMap(map[string]string{
		"INFERPLANED_OIDC_ISSUER":         "https://cognito-idp.ap-northeast-2.amazonaws.com/pool-1",
		"INFERPLANED_OIDC_CLIENT_ID":      "client-1",
		"INFERPLANED_OIDC_ALLOWED_GROUPS": "ops",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cs := o.connectSrc(); cs != nil {
		t.Fatalf("connectSrc must be nil when login_origins is empty (SSO browser flow off), got %v", cs)
	}

	o2, err := loadOIDCEnv(getenvMap(map[string]string{
		"INFERPLANED_OIDC_ISSUER":         "https://cognito-idp.ap-northeast-2.amazonaws.com/pool-1",
		"INFERPLANED_OIDC_CLIENT_ID":      "client-1",
		"INFERPLANED_OIDC_ALLOWED_GROUPS": "ops",
		"INFERPLANED_OIDC_LOGIN_ORIGINS":  "https://console.example.com",
	}))
	if err != nil {
		t.Fatal(err)
	}
	cs := o2.connectSrc()
	want := []string{"https://cognito-idp.ap-northeast-2.amazonaws.com", "https://console.example.com"}
	if len(cs) != 2 || cs[0] != want[0] || cs[1] != want[1] {
		t.Fatalf("connectSrc = %v, want %v (issuer PATH must be stripped to origin only)", cs, want)
	}
}
