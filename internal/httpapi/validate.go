package httpapi

import (
	"context"
	"net/http"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // the container image has no zoneinfo
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var tzRe = regexp.MustCompile(`^[A-Za-z_]+(/[A-Za-z0-9_+-]+){1,2}$`)

// validTZ accepts an IANA zone such as "Asia/Beirut" that this binary knows (time/tzdata is embedded).
func validTZ(s string) bool {
	if !tzRe.MatchString(s) {
		return false
	}
	_, err := time.LoadLocation(s)
	return err == nil
}

// Field limits shared by customers, suppliers and items (audit B-M3).
const (
	maxNameLen    = 200
	maxEmailLen   = 200
	maxPhoneLen   = 40
	maxAddressLen = 500
	maxNotesLen   = 4000
)

// attachmentQuota: files kept per company (they live in the database and in every backup; audit C-L2).
var attachmentQuota int64 = 1 << 30

// checkStorageQuota refuses a new file that would take the company's attachments above the quota.
func checkStorageQuota(ctx context.Context, tx pgx.Tx, cid string, add int) error {
	var used int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(sum(size), 0) FROM attachments WHERE company_id = $1 AND deleted_at IS NULL`, cid).Scan(&used); err != nil {
		return err
	}
	if used+int64(add) > attachmentQuota {
		return &apiError{status: http.StatusRequestEntityTooLarge, code: "storage_full", detail: strconv.FormatInt(attachmentQuota>>20, 10)}
	}
	return nil
}

// validEmail accepts an empty address or one plain "local@domain.tld" address.
func validEmail(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	if len(s) > maxEmailLen || strings.ContainsAny(s, " <>,;") {
		return false
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s {
		return false
	}
	at := strings.LastIndexByte(s, '@')
	return at > 0 && strings.Contains(s[at+1:], ".") && !strings.HasSuffix(s, ".")
}

func tooLong(p *string, n int) bool {
	return p != nil && utf8.RuneCountInString(strings.TrimSpace(*p)) > n
}

// partyFieldsErr returns an error code for bad contact fields ("" = fine).
func partyFieldsErr(name, email, phone, phone2, address, notes *string) string {
	switch {
	case tooLong(name, maxNameLen):
		return "name_too_long"
	case email != nil && !validEmail(*email):
		return "invalid_email"
	case tooLong(phone, maxPhoneLen) || tooLong(phone2, maxPhoneLen):
		return "phone_too_long"
	case tooLong(address, maxAddressLen):
		return "text_too_long"
	case tooLong(notes, maxNotesLen):
		return "text_too_long"
	}
	return ""
}
