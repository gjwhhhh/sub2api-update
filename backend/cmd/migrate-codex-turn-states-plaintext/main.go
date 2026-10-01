package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"log"

	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
)

type decryptor interface {
	Decrypt(string) (string, error)
}

func main() {
	execute := flag.Bool("execute", false, "write plaintext and clear legacy ciphertext")
	flag.Parse()

	cfg, err := config.LoadForBootstrap()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	client, db, err := repository.InitEnt(cfg)
	if err != nil {
		log.Fatalf("initialize database: %v", err)
	}
	defer func() { _ = client.Close() }()

	enc, err := repository.NewAESEncryptor(cfg)
	if err != nil {
		log.Fatalf("initialize decryptor: %v", err)
	}
	d, ok := enc.(decryptor)
	if !ok {
		log.Fatal("configured encryptor does not support decryption")
	}

	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `
		SELECT id, state_ciphertext, state_length, state_sha256
		FROM codex_turn_states
		WHERE state_plaintext IS NULL AND state_ciphertext IS NOT NULL
		ORDER BY id`)
	if err != nil {
		log.Fatalf("query legacy states: %v", err)
	}
	defer rows.Close()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Fatalf("begin transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	var scanned, migrated int
	for rows.Next() {
		var id int64
		var ciphertext string
		var expectedLength int
		var expectedSHA string
		if err := rows.Scan(&id, &ciphertext, &expectedLength, &expectedSHA); err != nil {
			log.Fatalf("scan state %d: %v", id, err)
		}
		scanned++
		plaintext, err := d.Decrypt(ciphertext)
		if err != nil {
			log.Fatalf("decrypt state %d: %v", id, err)
		}
		digest := sha256.Sum256([]byte(plaintext))
		if len([]byte(plaintext)) != expectedLength || !equalSHA(hex.EncodeToString(digest[:]), expectedSHA) {
			log.Fatalf("verification failed for state %d", id)
		}
		if *execute {
			if _, err := tx.ExecContext(ctx, `UPDATE codex_turn_states SET state_plaintext = $1, state_ciphertext = NULL, encryption_version = 0 WHERE id = $2`, plaintext, id); err != nil {
				log.Fatalf("migrate state %d: %v", id, err)
			}
		}
		migrated++
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("read legacy states: %v", err)
	}
	if *execute {
		if err := tx.Commit(); err != nil {
			log.Fatalf("commit migration: %v", err)
		}
	}
	mode := "dry-run"
	if *execute {
		mode = "execute"
	}
	fmt.Printf("mode=%s scanned=%d migrated=%d\n", mode, scanned, migrated)
}

func equalSHA(actual, expected string) bool {
	return len(expected) == 64 && actual == expected
}
