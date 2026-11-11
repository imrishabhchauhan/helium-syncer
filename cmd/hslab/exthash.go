package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/imrishabhchauhan/helium-syncer/internal/extensions"
)

// extHash packs each extension's data twice and prints both hashes. Two
// different hashes for a browser that never ran mean the sync sees a change
// that is not there, and sends it again on every run.
func extHash(profile string) error {
	ents, err := os.ReadDir(filepath.Join(profile, "Local Extension Settings"))
	if err != nil {
		return err
	}
	var ids []string
	for _, e := range ents {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	fmt.Printf("%-34s %-8s %-18s %-18s\n", "extension", "syncs", "hash 1", "hash 2")
	for _, id := range ids {
		syncs := extensions.SyncsData(profile, id)
		if !syncs {
			fmt.Printf("%-34s %-8v %s\n", id, syncs, "(data never sent)")
			continue
		}
		_, h1, e1 := extensions.PackDataHash(profile, id)
		_, h2, e2 := extensions.PackDataHash(profile, id)
		mark := "same"
		if h1 != h2 {
			mark = "DIFFERENT: the sync sees a change that is not there"
		}
		if e1 != nil || e2 != nil {
			mark = fmt.Sprint("error: ", e1, e2)
		}
		fmt.Printf("%-34s %-8v %-18s %-18s %s\n", id, syncs, short(h1), short(h2), mark)
	}
	return nil
}

func short(h string) string {
	if len(h) > 16 {
		return h[:16]
	}
	return h
}
