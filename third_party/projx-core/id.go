package core

// StableID builds the collision-free identity used by BOTH context deltas and
// verify snapshots — one scheme, owned here in core so neither layer has to
// import the other.
//
//	path::Recv.Name   for methods    e.g. "internal/server.go::Server.Start"
//	path::Name        for everything else
//
// It keys on path + (receiver) + name, NEVER on line number, so it survives edits
// above the declaration. Two same-named methods on different receiver types in one
// file get distinct IDs — which a bare path+name scheme would collide.
func StableID(path, recv, name string) string {
	if recv != "" {
		return path + "::" + recv + "." + name
	}
	return path + "::" + name
}
