package toolhub

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"maps"
	"slices"
	"strconv"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

type dbKey struct{ table, collection, id string }

func rowKey(table string, r dbRow) dbKey {
	id := r.id
	if table == "listrec" {
		id = strconv.Itoa(r.seq)
	}
	return dbKey{table, r.collection, id}
}

func rowHash(table string, r dbRow) [32]byte {
	h := sha256.New()
	if table == "listrec" {
		_ = binary.Write(h, binary.BigEndian, int64(r.seq))
	}
	_, _ = h.Write(r.body)
	return [32]byte(h.Sum(nil))
}

// The digest covers identities, history order and content; it does not serialize the
// immutable history again on every connection health observation.
func rowsDigest(index map[dbKey][32]byte, installation string) [32]byte {
	keys := slices.Collect(maps.Keys(index))
	slices.SortFunc(keys, func(a, b dbKey) int {
		for _, pair := range [][2]string{{a.table, b.table}, {a.collection, b.collection}, {a.id, b.id}} {
			if pair[0] < pair[1] {
				return -1
			}
			if pair[0] > pair[1] {
				return 1
			}
		}
		return 0
	})
	h := sha256.New()
	_, _ = h.Write([]byte(installation + "\x00"))
	for _, key := range keys {
		_, _ = h.Write([]byte(key.table + "\x00" + key.collection + "\x00" + key.id + "\x00"))
		sum := index[key]
		_, _ = h.Write(sum[:])
	}
	return [32]byte(h.Sum(nil))
}

func dbRowsFor(state snapshot, previous map[dbKey][32]byte, counts map[string]int) (map[dbKey][32]byte, map[dbKey]dbRow, map[string]int, error) {
	changes, bindings := state.CapabilityChanges, state.BindingChanges
	if counts["capability_change"] > len(changes) || counts["binding_change"] > len(bindings) {
		return nil, nil, nil, api.ErrConflict
	}
	state.CapabilityChanges, state.BindingChanges = nil, nil
	ents, kvs, _, err := snapshotRows(state)
	if err != nil {
		return nil, nil, nil, err
	}
	index := make(map[dbKey][32]byte)
	writes := make(map[dbKey]dbRow)
	add := func(table string, row dbRow) {
		key, sum := rowKey(table, row), rowHash(table, row)
		index[key] = sum
		if old, ok := previous[key]; !ok || old != sum {
			writes[key] = row
		}
	}
	for _, row := range ents {
		add("ent", row)
	}
	for _, row := range kvs {
		add("kvmap", row)
	}
	// History is append-only. Existing SQL rows remain untouched, including
	// during the first save of a legacy snapshot-digest database.
	nextCounts := map[string]int{"capability_change": len(changes), "binding_change": len(bindings)}
	for key, sum := range previous {
		if key.table == "listrec" {
			seq, _ := strconv.Atoi(key.id)
			if seq < nextCounts[key.collection] {
				index[key] = sum
			}
		}
	}
	for collection, length := range nextCounts {
		for seq := counts[collection]; seq < length; seq++ {
			var body []byte
			if collection == "capability_change" {
				body, err = json.Marshal(changes[seq])
			} else {
				body, err = json.Marshal(bindings[seq])
			}
			if err != nil {
				return nil, nil, nil, err
			}
			add("listrec", dbRow{collection: collection, seq: seq, body: body})
		}
	}
	return index, writes, nextCounts, nil
}
