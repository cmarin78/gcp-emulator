// Package storage provee una capa de persistencia embebida (BoltDB) para
// que el emulador sea completamente portable: un único archivo de datos,
// sin dependencias externas (no requiere Postgres, Docker, etc.).
package storage

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	bolt "go.etcd.io/bbolt"
)

// DB envuelve una base de datos BoltDB y expone operaciones genéricas
// de tipo key/value con buckets, usadas por todos los servicios emulados
// (IAM, GCS, Compute, etc.).
type DB struct {
	bolt *bolt.DB
}

// Open abre (o crea) el archivo de base de datos en la ruta indicada.
func Open(path string) (*DB, error) {
	b, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("storage: no se pudo abrir la base de datos %q: %w", path, err)
	}
	return &DB{bolt: b}, nil
}

// Close cierra la base de datos.
func (d *DB) Close() error {
	return d.bolt.Close()
}

// EnsureBucket crea el bucket si no existe.
func (d *DB) EnsureBucket(bucket string) error {
	return d.bolt.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(bucket))
		return err
	})
}

// Put serializa value como JSON y lo guarda bajo bucket/key.
func (d *DB) Put(bucket, key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("storage: error serializando valor: %w", err)
	}
	return d.bolt.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(bucket))
		if err != nil {
			return err
		}
		return b.Put([]byte(key), data)
	})
}

// Get busca bucket/key y deserializa el resultado en out (puntero).
// Devuelve found=false si la clave no existe.
func (d *DB) Get(bucket, key string, out any) (found bool, err error) {
	err = d.bolt.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return nil
		}
		data := b.Get([]byte(key))
		if data == nil {
			return nil
		}
		found = true
		return json.Unmarshal(data, out)
	})
	return found, err
}

// Delete elimina bucket/key. No falla si la clave no existe.
func (d *DB) Delete(bucket, key string) error {
	return d.bolt.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return nil
		}
		return b.Delete([]byte(key))
	})
}

// List recorre todas las entradas de un bucket cuyo key tenga el prefijo
// dado (usar "" para listar todo) y llama a fn(key, rawJSON) por cada una.
// Si fn devuelve un error, List se detiene y propaga ese error.
func (d *DB) List(bucket, prefix string, fn func(key string, raw []byte) error) error {
	return d.bolt.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return nil
		}
		c := b.Cursor()
		p := []byte(prefix)
		for k, v := c.Seek(p); k != nil && hasPrefix(k, p); k, v = c.Next() {
			if err := fn(string(k), v); err != nil {
				return err
			}
		}
		return nil
	})
}

// WriteSnapshot writes a point-in-time binary snapshot of BoltDB to w.
func (d *DB) WriteSnapshot(w io.Writer) error {
	return d.bolt.View(func(tx *bolt.Tx) error {
		_, err := tx.WriteTo(w)
		return err
	})
}

// ExportJSON exports all buckets and keys as a nested map: bucket -> key -> raw JSON.
func (d *DB) ExportJSON() (map[string]map[string]json.RawMessage, error) {
	out := make(map[string]map[string]json.RawMessage)
	err := d.bolt.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(name []byte, b *bolt.Bucket) error {
			bName := string(name)
			out[bName] = make(map[string]json.RawMessage)
			return b.ForEach(func(k, v []byte) error {
				raw := make(json.RawMessage, len(v))
				copy(raw, v)
				out[bName][string(k)] = raw
				return nil
			})
		})
	})
	return out, err
}

// ImportJSON imports data into buckets.
func (d *DB) ImportJSON(data map[string]map[string]json.RawMessage) error {
	return d.bolt.Update(func(tx *bolt.Tx) error {
		for bName, items := range data {
			b, err := tx.CreateBucketIfNotExists([]byte(bName))
			if err != nil {
				return err
			}
			for k, v := range items {
				if err := b.Put([]byte(k), v); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func hasPrefix(k, prefix []byte) bool {
	if len(prefix) == 0 {
		return true
	}
	if len(k) < len(prefix) {
		return false
	}
	for i := range prefix {
		if k[i] != prefix[i] {
			return false
		}
	}
	return true
}
