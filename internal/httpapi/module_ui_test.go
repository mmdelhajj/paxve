package httpapi

import (
	"encoding/json"
	"testing"
)

// Appearance: per-user prefs, organisation default for users who never chose, validation, clear back to default.
func TestUIPrefs(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	prefs := func(c *client) map[string]any {
		t.Helper()
		me := c.ok("GET", "/me", nil, 200)
		if me.body["ui_prefs"] == nil {
			return nil
		}
		return obj(me.body["ui_prefs"])
	}
	if p := prefs(o.c); p != nil {
		t.Fatalf("new user has prefs %v", p)
	}
	// own choice
	o.c.ok("PATCH", "/me", map[string]any{"ui_prefs": map[string]string{"theme": "dark", "size": "l", "density": "comfy"}}, 200)
	if p := prefs(o.c); str(p["theme"]) != "dark" || str(p["size"]) != "l" || str(p["density"]) != "comfy" {
		t.Fatalf("prefs %v", p)
	}
	// a PATCH without ui_prefs keeps them
	o.c.ok("PATCH", "/me", map[string]any{"locale": "fr"}, 200)
	if p := prefs(o.c); str(p["theme"]) != "dark" {
		t.Fatalf("prefs lost on another change: %v", p)
	}
	// bad values refused
	for _, bad := range []any{map[string]string{"theme": "pink"}, map[string]string{"size": "xxl"}, map[string]string{"density": "x"}, "dark"} {
		if r := o.c.do("PATCH", "/me", map[string]any{"ui_prefs": bad}); r.status != 400 {
			t.Fatalf("bad prefs %v: %d", bad, r.status)
		}
	}
	// organisation default: applies to a user who has not chosen, not to one who has
	o.c.ok("PUT", "/org/ui-default", map[string]string{"theme": "contrast", "size": "xl"}, 200)
	if r := o.c.do("PUT", "/org/ui-default", map[string]string{"theme": "neon"}); r.status != 400 {
		t.Fatalf("bad default: %d", r.status)
	}
	email := "ui" + randHex(4) + "@example.com"
	o.c.ok("POST", "/users", map[string]any{"email": email, "full_name": "Clerk", "role": "data_entry", "locale": "ar", "password": "clerk-secret-9"}, 201)
	u := loginAs(t, email, "clerk-secret-9")
	if p := prefs(u); str(p["theme"]) != "contrast" || str(p["size"]) != "xl" {
		t.Fatalf("default not applied: %v", p)
	}
	if p := prefs(o.c); str(p["theme"]) != "dark" {
		t.Fatalf("default overrode own choice: %v", p)
	}
	// only admins (users.edit) set the default; everyone may read it
	if r := u.do("PUT", "/org/ui-default", map[string]string{"theme": "classic"}); r.status != 403 {
		t.Fatalf("clerk set default: %d", r.status)
	}
	d := u.ok("GET", "/org/ui-default", nil, 200)
	if str(d.body["theme"]) != "contrast" {
		t.Fatalf("read default: %s", d.raw)
	}
	// the user's own choice, then null = back to the default
	u.ok("PATCH", "/me", map[string]any{"ui_prefs": map[string]string{"theme": "comfort"}}, 200)
	if p := prefs(u); str(p["theme"]) != "comfort" {
		t.Fatalf("own %v", p)
	}
	u.ok("PATCH", "/me", json.RawMessage(`{"ui_prefs": null}`), 200)
	if p := prefs(u); str(p["theme"]) != "contrast" {
		t.Fatalf("cleared prefs should fall back to default: %v", p)
	}
}
