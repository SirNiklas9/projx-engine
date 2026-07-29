package store

// sqlConn is the minimal SQL primitive the SQLite store sits on — exec for writes,
// query for reads (rows as [][]any, columns positional). It is the seam that lets
// the store run TWO ways from one schema: locally on modernc.org/sqlite (native
// build) or as a Pulp cell calling the host's storage.sqlite capability (wasm
// build). Neither the store logic nor the migrations know which backend they have.
type sqlConn interface {
	exec(query string, args ...any) error
	query(query string, args ...any) ([][]any, error)
	close() error
}

// asText coerces a driver/msgpack cell value to string. SQLite TEXT comes back as
// string from modernc and as string from msgpack, but []byte is possible on either
// path, so both are handled; anything else (incl. nil) becomes "".
func asText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return ""
	}
}

// asInt coerces a driver/msgpack cell value to int. SQLite INTEGER arrives as int64
// from modernc; msgpack may decode it as int64/uint64/int. All are handled.
// Use asInt64 for columns that must not lose the upper 32 bits (e.g. updated_at).
func asInt(v any) int {
	switch t := v.(type) {
	case int64:
		return int(t)
	case uint64:
		return int(t)
	case int:
		return t
	case int32:
		return int(t)
	case uint32:
		return int(t)
	case float64:
		return int(t)
	default:
		return 0
	}
}

// asInt64 coerces a driver/msgpack cell value to int64 without a narrowing cast.
// Required for updated_at, which is unix milliseconds and overflows int on 32-bit
// after year 2038 (int is platform-sized; int64 is always 64-bit).
func asInt64(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case uint64:
		return int64(t)
	case int:
		return int64(t)
	case int32:
		return int64(t)
	case uint32:
		return int64(t)
	case float64:
		return int64(t)
	default:
		return 0
	}
}

// rowToRecord maps the SELECT column order used by SQLite to a Record. It tolerates
// shorter legacy rows by leaving metadata at its compatibility zero value.
func rowToRecord(row []any) Record {
	if len(row) < 5 {
		return Record{}
	}
	r := Record{
		ID:    asText(row[0]),
		Kind:  Kind(asInt(row[1])),
		Scope: Scope(asInt(row[2])),
		Key:   asText(row[3]),
		Body:  asText(row[4]),
	}
	if len(row) >= 7 {
		r.UpdatedAt = asInt64(row[5])
		r.Origin = asText(row[6])
	}
	if len(row) >= 8 {
		r.Enforcement = asText(row[7])
	}
	if len(row) >= 9 {
		r.Provenance = asText(row[8])
	}
	if len(row) >= 20 {
		r.Status = asText(row[9])
		r.Supersedes = asText(row[10])
		r.ReplacedBy = asText(row[11])
		r.ClaimClass = asText(row[12])
		r.VerifiedAt = asInt64(row[13])
		r.ReviewAfter = asInt64(row[14])
		r.Verifier = asText(row[15])
		r.Evidence = asText(row[16])
		r.Model = asText(row[17])
		r.Confidence = asInt(row[18])
		r.Approval = asText(row[19])
	}
	return r
}
