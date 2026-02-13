package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/dgraph-io/badger/v4"
)

func main() {
	db, err := badger.Open(badger.DefaultOptions("./crwal_db").WithReadOnly(true))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	err = db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()

		i := 0
		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()

			val, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}

			gr, err := gzip.NewReader(bytes.NewReader(val))
			if err != nil {
				return err
			}

			htmlBytes, err := io.ReadAll(gr)
			gr.Close()
			if err != nil {
				return err
			}

			filename := fmt.Sprintf("page_%d.html", i)
			if err := os.WriteFile(filename, htmlBytes, 0644); err != nil {
				return err
			}

			fmt.Println("Saved:", filename)

			i++
			// if i == 3 { // export first 3 pages
			// 	break
			// }
		}
		return nil
	})

	if err != nil {
		log.Fatal(err)
	}
}
