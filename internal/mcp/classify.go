package mcp

import (
	"strings"
	"unicode"
)

// Class is the kind of effect that an MCP tool has. The approval gate reads it.
type Class string

const (
	// ClassRead is a tool that only reads. The gate allows it without a prompt.
	ClassRead Class = "read"
	// ClassWrite is a tool that changes data. The gate asks the user.
	ClassWrite Class = "write"
	// ClassSend is a tool that sends data to other people. The gate asks the user.
	ClassSend Class = "send"
	// ClassHumanOnly is a tool that only the human owner can approve. No grant covers it.
	ClassHumanOnly Class = "human-only"
)

// The word lists below are new text of this project. They do not copy another agent.
var (
	readVerbs = wordSet("get list fetch search read find retrieve lookup view check count query describe show download")
	sendWords = wordSet("send reply respond forward post publish tweet retweet broadcast invite share")
	createWds = wordSet("create add new")
	sentNouns = wordSet("message comment post email mail invoice reply tweet dm")
	writeWds  = wordSet("create update edit set put patch delete remove move rename upload insert append write run execute approve reject archive merge close")

	payFirst     = wordSet("pay purchase buy checkout")
	payVerbs     = wordSet("create issue send make capture initiate transfer")
	payNouns     = wordSet("payment charge refund payout funds money")
	accountVerbs = wordSet("create add invite")
	accountNouns = wordSet("account user member collaborator")
	credVerbs    = wordSet("create generate rotate reveal")
	credNouns    = wordSet("token key secret credential password")
	passVerbs    = wordSet("change reset update set")
	purgeWords   = wordSet("purge permanently destroy wipe")
	trashNouns   = wordSet("trash bin")
	deleteVerbs  = wordSet("delete remove")
	bulkWords    = wordSet("all bulk batch many")
)

func wordSet(s string) map[string]bool {
	m := map[string]bool{}
	for w := range strings.FieldsSeq(s) {
		m[w] = true
	}
	return m
}

// splitWords splits a name on "_", "-", ".", and camelCase boundaries.
// It lower-cases each word and drops empty words.
func splitWords(s string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	var prev rune
	for _, r := range s {
		switch {
		case r == '_' || r == '-' || r == '.':
			flush()
		case unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsDigit(prev)):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
		prev = r
	}
	flush()
	return words
}

func hasAny(words []string, set map[string]bool) bool {
	for _, w := range words {
		if set[w] {
			return true
		}
	}
	return false
}

// humanOnlyWords reports whether the words match one of the six human-only sets.
func humanOnlyWords(words []string) bool {
	if len(words) > 0 && payFirst[words[0]] {
		return true
	}
	switch {
	case hasAny(words, payVerbs) && hasAny(words, payNouns),
		hasAny(words, accountVerbs) && hasAny(words, accountNouns),
		hasAny(words, credVerbs) && hasAny(words, credNouns),
		hasAny(words, passVerbs) && hasAny(words, map[string]bool{"password": true}),
		hasAny(words, purgeWords),
		hasAny(words, map[string]bool{"empty": true}) && hasAny(words, trashNouns),
		hasAny(words, deleteVerbs) && hasAny(words, bulkWords):
		return true
	}
	return false
}

// Classify gives an MCP tool one class. It takes the raw tool name, the server
// name, and the hints of the server.
func Classify(rawName, server string, readOnly bool, destructive *bool) Class {
	words := splitWords(rawName)
	prefix := map[string]bool{"api": true, "mcp": true}
	for _, w := range splitWords(server) {
		prefix[w] = true
	}
	for len(words) > 0 && prefix[words[0]] {
		wasAPI := words[0] == "api"
		words = words[1:]
		if wasAPI && len(words) > 0 && (words[0] == "post" || words[0] == "put") {
			words = words[1:]
		}
	}
	destructiveTrue := destructive != nil && *destructive

	switch {
	case humanOnlyWords(words):
		return ClassHumanOnly
	case len(words) > 0 && readVerbs[words[0]] && !destructiveTrue:
		return ClassRead
	case hasAny(words, sendWords):
		return ClassSend
	case hasAny(words, map[string]bool{"draft": true}):
		return ClassWrite
	case hasAny(words, createWds) && hasAny(words, sentNouns):
		return ClassSend
	case readOnly && !destructiveTrue && !hasAny(words, writeWds):
		return ClassRead
	}
	return ClassWrite
}
