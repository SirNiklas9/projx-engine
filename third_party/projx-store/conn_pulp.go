//go:build wasm

package store

import (
	"fmt"
	"runtime"
	"unsafe"

	"github.com/vmihailenco/msgpack/v5"
)

// This is the FLIP for the store: compiled as a Pulp cell (GOARCH=wasm), the store
// carries no SQLite engine. It calls the host's storage.sqlite capability —
// Pulp-ext-sqlite — over two wasm imports. The host runs modernc natively and owns
// the DB file (<storage-root>/<cell>/data.db); the cell just sends SQL + msgpack
// params and reads msgpack rows back. Same schema, same store logic, zero embedded DB.
//
// The cell binary must export pulp_alloc (the host allocates the response inside the
// cell through it) and declare the "storage.sqlite" capability in its manifest. A
// Pulp host built without Pulp-ext-sqlite stubs these imports (error 99) — the cell
// still loads; queries just fail loudly.

//go:wasmimport pulp sqlite_exec
func hostSqliteExec(qPtr, qLen, pPtr, pLen, resPtrOut, resLenOut uint32) uint32

//go:wasmimport pulp sqlite_query
func hostSqliteQuery(qPtr, qLen, pPtr, pLen, rowsPtrOut, rowsLenOut uint32) uint32

// execResult / queryResult mirror Pulp-ext-sqlite's msgpack wire shapes.
type execResult struct {
	RowsAffected int64  `msgpack:"rows_affected"`
	LastInsertID int64  `msgpack:"last_insert_id"`
	Error        string `msgpack:"error,omitempty"`
}

type queryResult struct {
	Columns []string `msgpack:"columns"`
	Rows    [][]any  `msgpack:"rows"`
	Error   string   `msgpack:"error,omitempty"`
}

// pulpConn satisfies sqlConn by delegating to the host capability.
type pulpConn struct{}

// openConn ignores path: the host decides the DB location per cell. There is nothing
// to open here — the host opened (or stubbed) the DB at cell registration.
func openConn(_ string) (sqlConn, error) { return pulpConn{}, nil }

func (pulpConn) close() error { return nil } // the host owns the DB lifecycle.

func (pulpConn) exec(query string, args ...any) error {
	resp, code, err := call(hostSqliteExec, query, args)
	if err != nil {
		return err
	}
	if len(resp) > 0 {
		var r execResult
		if msgpack.Unmarshal(resp, &r) == nil && r.Error != "" {
			return fmt.Errorf("store: sqlite exec: %s", r.Error)
		}
	}
	if code != 0 {
		return fmt.Errorf("store: sqlite exec host code %d", code)
	}
	return nil
}

func (pulpConn) query(query string, args ...any) ([][]any, error) {
	resp, code, err := call(hostSqliteQuery, query, args)
	if err != nil {
		return nil, err
	}
	if len(resp) > 0 {
		var r queryResult
		if e := msgpack.Unmarshal(resp, &r); e != nil {
			return nil, fmt.Errorf("store: sqlite query decode: %w", e)
		} else if r.Error != "" {
			return nil, fmt.Errorf("store: sqlite query: %s", r.Error)
		} else {
			return r.Rows, nil
		}
	}
	if code != 0 {
		return nil, fmt.Errorf("store: sqlite query host code %d", code)
	}
	return nil, nil
}

// call marshals args to msgpack, invokes a host import, and reads the response the
// host wrote back through pulp_alloc. The host writes (ptr,len) into outPtr/outLen,
// which point at our linear memory; we then read the bytes at that ptr.
func call(
	host func(qPtr, qLen, pPtr, pLen, outPtr, outLen uint32) uint32,
	query string, args []any,
) (resp []byte, code uint32, err error) {
	qb := []byte(query)
	var pb []byte
	if len(args) > 0 {
		pb, err = msgpack.Marshal(args)
		if err != nil {
			return nil, 0, fmt.Errorf("store: marshal params: %w", err)
		}
	}
	var outPtr, outLen uint32
	code = host(
		bytePtr(qb), uint32(len(qb)),
		bytePtr(pb), uint32(len(pb)),
		u32Ptr(&outPtr), u32Ptr(&outLen),
	)
	// Keep the argument buffers alive across the host call (their addresses are in
	// use by the host while it reads guest memory).
	runtime.KeepAlive(qb)
	runtime.KeepAlive(pb)
	return readMem(outPtr, outLen), code, nil
}

// bytePtr returns the linear-memory address of a byte slice's backing array (0 for
// empty). Valid only on GOARCH=wasm, where pointers are 32-bit.
func bytePtr(b []byte) uint32 {
	if len(b) == 0 {
		return 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0])))
}

// u32Ptr returns the address of a uint32 for use as a host out-parameter.
func u32Ptr(p *uint32) uint32 { return uint32(uintptr(unsafe.Pointer(p))) }

// readMem reads ln bytes of guest linear memory starting at ptr.
func readMem(ptr, ln uint32) []byte {
	if ln == 0 || ptr == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), ln)
}
