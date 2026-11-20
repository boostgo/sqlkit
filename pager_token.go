package sqlkit

import (
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"time"
)

const (
	tokenVersion   = 1
	maxTokenLength = 256

	tokenModeCursor = "c"
	tokenModeOffset = "o"
)

// pagerToken is the wire format of the v1 page token (base64url of compact JSON):
//
//	{"v":1,"m":"c","f":"<fingerprint>","k":["i:42","t:2026-11-11T10:00:00.123456Z"]}
//	{"v":1,"m":"o","f":"<fingerprint>","offset":100}
//
// The token is not signed: it is only structurally validated, and values reach SQL as bind parameters only.
type pagerToken struct {
	V      int      `json:"v"`
	M      string   `json:"m"`
	F      string   `json:"f"`
	K      []string `json:"k,omitempty"`
	Offset *int     `json:"offset,omitempty"`

	values []any // decoded K, filled by decodeToken
}

// fingerprint returns 8 hex chars of FNV-1a 32 over the key list, e.g. "created_at:d,id:d".
func fingerprint(keys []pagerKey) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		dir := "a"
		if k.desc {
			dir = "d"
		}

		parts[i] = k.column + ":" + dir
	}

	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.Join(parts, ",")))

	return fmt.Sprintf("%08x", h.Sum32())
}

func encodeToken(t pagerToken) (string, error) {
	raw, err := json.Marshal(t)
	if err != nil {
		return "", err
	}

	token := base64.RawURLEncoding.EncodeToString(raw)
	if len(token) > maxTokenLength {
		return "", fmt.Errorf("%w: token is %d bytes, max is %d", ErrInvalidCursor, len(token), maxTokenLength)
	}

	return token, nil
}

func encodeCursorToken(fp string, values []any) (string, error) {
	encoded := make([]string, len(values))
	for i, v := range values {
		s, err := encodeTokenValue(v)
		if err != nil {
			return "", err
		}

		encoded[i] = s
	}

	return encodeToken(pagerToken{V: tokenVersion, M: tokenModeCursor, F: fp, K: encoded})
}

func encodeOffsetToken(fp string, offset int) (string, error) {
	return encodeToken(pagerToken{V: tokenVersion, M: tokenModeOffset, F: fp, Offset: &offset})
}

// decodeToken structurally validates a token. Every failure is wrapped with ErrInvalidCursor.
func decodeToken(token string) (pagerToken, error) {
	var t pagerToken

	if len(token) > maxTokenLength {
		return t, fmt.Errorf("%w: too long", ErrInvalidCursor)
	}

	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return t, fmt.Errorf("%w: not base64url", ErrInvalidCursor)
	}

	if err = json.Unmarshal(raw, &t); err != nil {
		return pagerToken{}, fmt.Errorf("%w: not json", ErrInvalidCursor)
	}

	if t.V != tokenVersion {
		return pagerToken{}, fmt.Errorf("%w: unsupported version", ErrInvalidCursor)
	}

	switch t.M {
	case tokenModeCursor:
		t.values = make([]any, len(t.K))
		for i, s := range t.K {
			if t.values[i], err = decodeTokenValue(s); err != nil {
				return pagerToken{}, err
			}
		}
	case tokenModeOffset:
		if t.Offset == nil || *t.Offset < 0 {
			return pagerToken{}, fmt.Errorf("%w: bad offset", ErrInvalidCursor)
		}
	default:
		return pagerToken{}, fmt.Errorf("%w: unknown mode", ErrInvalidCursor)
	}

	return t, nil
}

// encodeTokenValue encodes a key value as "<tag>:<value>" (NULL is "n").
// Types go through driver.DefaultParameterConverter, so driver.Valuer (e.g. uuid.UUID) is supported.
func encodeTokenValue(v any) (string, error) {
	cv, err := driver.DefaultParameterConverter.ConvertValue(v)
	if err != nil {
		return "", fmt.Errorf("sqlkit.pager: unsupported key value %T: %w", v, err)
	}

	switch x := cv.(type) {
	case nil:
		return "n", nil
	case int64:
		return "i:" + strconv.FormatInt(x, 10), nil
	case float64:
		return "f:" + strconv.FormatFloat(x, 'g', -1, 64), nil
	case bool:
		return "b:" + strconv.FormatBool(x), nil
	case string:
		return "s:" + x, nil
	case []byte:
		return "x:" + base64.RawURLEncoding.EncodeToString(x), nil
	case time.Time:
		return "t:" + x.Format(time.RFC3339Nano), nil
	default:
		return "", fmt.Errorf("sqlkit.pager: unsupported key value %T", v)
	}
}

func decodeTokenValue(s string) (any, error) {
	if s == "n" {
		return nil, nil //nolint:nilnil // NULL is a valid decoded value
	}

	tag, val, ok := strings.Cut(s, ":")
	if !ok {
		return nil, fmt.Errorf("%w: bad value", ErrInvalidCursor)
	}

	var (
		out any
		err error
	)

	switch tag {
	case "i":
		out, err = strconv.ParseInt(val, 10, 64)
	case "f":
		out, err = strconv.ParseFloat(val, 64)
	case "b":
		out, err = strconv.ParseBool(val)
	case "s":
		out = val
	case "x":
		out, err = base64.RawURLEncoding.DecodeString(val)
	case "t":
		out, err = time.Parse(time.RFC3339Nano, val)
	default:
		return nil, fmt.Errorf("%w: unknown value tag", ErrInvalidCursor)
	}

	if err != nil {
		return nil, fmt.Errorf("%w: bad value", ErrInvalidCursor)
	}

	return out, nil
}
