package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/bytelope/zestmem/internal/buildinfo"
	"github.com/jackc/pgx/v5"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(buildinfo.String())
		return
	}
	if len(os.Args) < 2 {
		usage()
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, required("DATABASE_URL"))
	fatal(err)
	defer conn.Close(ctx)
	switch os.Args[1] {
	case "bootstrap":
		bootstrap(ctx, conn, os.Args[2:])
	case "create-principal":
		createPrincipal(ctx, conn, os.Args[2:])
	case "create-space":
		createSpace(ctx, conn, os.Args[2:])
	case "grant":
		grant(ctx, conn, os.Args[2:])
	case "bind-workspace":
		bind(ctx, conn, os.Args[2:])
	case "issue-key":
		issueKey(ctx, conn, os.Args[2:])
	case "revoke-key":
		revokeKey(ctx, conn, os.Args[2:])
	default:
		usage()
	}
}

func bootstrap(ctx context.Context, conn *pgx.Conn, args []string) {
	fs := flag.NewFlagSet("bootstrap", flag.ExitOnError)
	name := fs.String("name", "First Owner", "owner display name")
	org := fs.String("org", "Organization", "organization name")
	slug := fs.String("slug", "org", "organization slug")
	issuer := fs.String("issuer", "", "OIDC issuer")
	subject := fs.String("subject", "", "OIDC subject")
	fs.Parse(args)
	tx, err := conn.Begin(ctx)
	fatal(err)
	defer tx.Rollback(ctx)
	var principalID, spaceID string
	err = tx.QueryRow(ctx, `INSERT INTO principals(type,display_name,external_issuer,external_subject) VALUES('human',$1,NULLIF($2,''),NULLIF($3,'')) RETURNING id::text`, *name, *issuer, *subject).Scan(&principalID)
	fatal(err)
	err = tx.QueryRow(ctx, `INSERT INTO spaces(type,name,slug) VALUES('organization',$1,$2) RETURNING id::text`, *org, *slug).Scan(&spaceID)
	fatal(err)
	_, err = tx.Exec(ctx, `INSERT INTO space_grants(principal_id,space_id,role,granted_by) VALUES($1,$2,'owner',$1)`, principalID, spaceID)
	fatal(err)
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,action,target_type,target_id,reason) VALUES($1,'bootstrap_owner','space',$2,'initial operator recovery')`, principalID, spaceID)
	fatal(err)
	fatal(tx.Commit(ctx))
	fmt.Printf("principal_id=%s\nspace_id=%s\n", principalID, spaceID)
}

func createPrincipal(ctx context.Context, conn *pgx.Conn, args []string) {
	fs := flag.NewFlagSet("create-principal", flag.ExitOnError)
	name := fs.String("name", "", "display name")
	typ := fs.String("type", "agent", "human or agent")
	issuer := fs.String("issuer", "", "OIDC issuer")
	subject := fs.String("subject", "", "OIDC subject")
	by := fs.String("by", "", "creating Principal UUID")
	fs.Parse(args)
	tx, err := conn.Begin(ctx)
	fatal(err)
	defer tx.Rollback(ctx)
	requireOwner(ctx, tx, *by)
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO principals(type,display_name,external_issuer,external_subject) VALUES($1,$2,NULLIF($3,''),NULLIF($4,'')) RETURNING id::text`, *typ, *name, *issuer, *subject).Scan(&id)
	fatal(err)
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,action,target_type,target_id) VALUES($1,'create_principal','principal',$2)`, *by, id)
	fatal(err)
	fatal(tx.Commit(ctx))
	fmt.Println(id)
}

func createSpace(ctx context.Context, conn *pgx.Conn, args []string) {
	fs := flag.NewFlagSet("create-space", flag.ExitOnError)
	name := fs.String("name", "", "name")
	slug := fs.String("slug", "", "slug")
	typ := fs.String("type", "project", "organization, team, project, or private")
	parent := fs.String("parent", "", "parent UUID")
	by := fs.String("by", "", "creating Principal UUID")
	fs.Parse(args)
	tx, err := conn.Begin(ctx)
	fatal(err)
	defer tx.Rollback(ctx)
	requireOwner(ctx, tx, *by)
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO spaces(type,parent_id,name,slug) VALUES($1,NULLIF($2,'')::uuid,$3,$4) RETURNING id::text`, *typ, *parent, *name, *slug).Scan(&id)
	fatal(err)
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,action,target_type,target_id) VALUES($1,'create_space','space',$2)`, *by, id)
	fatal(err)
	fatal(tx.Commit(ctx))
	fmt.Println(id)
}

func grant(ctx context.Context, conn *pgx.Conn, args []string) {
	fs := flag.NewFlagSet("grant", flag.ExitOnError)
	principal := fs.String("principal", "", "principal UUID")
	space := fs.String("space", "", "Space UUID")
	role := fs.String("role", "reader", "reader, contributor, maintainer, or owner")
	by := fs.String("by", "", "granting Principal UUID")
	fs.Parse(args)
	tx, err := conn.Begin(ctx)
	fatal(err)
	defer tx.Rollback(ctx)
	requireOwner(ctx, tx, *by)
	_, err = tx.Exec(ctx, `INSERT INTO space_grants(principal_id,space_id,role,granted_by) VALUES($1,$2,$3,$4) ON CONFLICT(principal_id,space_id) DO UPDATE SET role=excluded.role,granted_by=excluded.granted_by`, *principal, *space, *role, *by)
	fatal(err)
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,action,target_type,target_id,metadata) VALUES($1,'grant_space','space',$2,jsonb_build_object('principal_id',$3::text,'role',$4::text))`, *by, *space, *principal, *role)
	fatal(err)
	fatal(tx.Commit(ctx))
	fmt.Println("ok")
}

func bind(ctx context.Context, conn *pgx.Conn, args []string) {
	fs := flag.NewFlagSet("bind-workspace", flag.ExitOnError)
	space := fs.String("space", "", "project Space UUID")
	kind := fs.String("kind", "git_remote", "git_remote or local_path")
	value := fs.String("value", "", "claim value")
	by := fs.String("by", "", "creating Principal UUID")
	fs.Parse(args)
	normalized := normalizeClaim(*kind, *value)
	tx, err := conn.Begin(ctx)
	fatal(err)
	defer tx.Rollback(ctx)
	requireOwner(ctx, tx, *by)
	var bindingID string
	err = tx.QueryRow(ctx, `INSERT INTO workspace_bindings(project_space_id,claim_kind,normalized_value,created_by) VALUES($1,$2,$3,$4) RETURNING id::text`, *space, *kind, normalized, *by).Scan(&bindingID)
	fatal(err)
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,action,target_type,target_id,metadata) VALUES($1,'bind_workspace','workspace_binding',$2,jsonb_build_object('space_id',$3::text,'kind',$4::text))`, *by, bindingID, *space, *kind)
	fatal(err)
	fatal(tx.Commit(ctx))
	fmt.Println("ok")
}

func issueKey(ctx context.Context, conn *pgx.Conn, args []string) {
	fs := flag.NewFlagSet("issue-key", flag.ExitOnError)
	principal := fs.String("principal", "", "agent Principal UUID")
	by := fs.String("by", "", "issuing Principal UUID")
	fs.Parse(args)
	raw := make([]byte, 32)
	_, err := rand.Read(raw)
	fatal(err)
	secret := "am_" + base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(secret))
	prefix := secret[:11]
	tx, err := conn.Begin(ctx)
	fatal(err)
	defer tx.Rollback(ctx)
	requireOwner(ctx, tx, *by)
	var keyID string
	err = tx.QueryRow(ctx, `INSERT INTO api_keys(principal_id,prefix,digest,created_by) VALUES($1,$2,$3,$4) RETURNING id::text`, *principal, prefix, sum[:], *by).Scan(&keyID)
	fatal(err)
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,action,target_type,target_id,metadata) VALUES($1,'issue_api_key','api_key',$2,jsonb_build_object('principal_id',$3::text,'prefix',$4::text))`, *by, keyID, *principal, prefix)
	fatal(err)
	fatal(tx.Commit(ctx))
	fmt.Printf("key=%s\nprefix=%s\nStore this value now; it cannot be recovered.\n", secret, prefix)
}

func revokeKey(ctx context.Context, conn *pgx.Conn, args []string) {
	fs := flag.NewFlagSet("revoke-key", flag.ExitOnError)
	prefix := fs.String("prefix", "", "key prefix shown at issuance")
	by := fs.String("by", "", "revoking Principal UUID")
	fs.Parse(args)
	tx, err := conn.Begin(ctx)
	fatal(err)
	defer tx.Rollback(ctx)
	requireOwner(ctx, tx, *by)
	var keyID string
	err = tx.QueryRow(ctx, `UPDATE api_keys SET revoked_at=now() WHERE prefix=$1 AND revoked_at IS NULL RETURNING id::text`, *prefix).Scan(&keyID)
	if errors.Is(err, pgx.ErrNoRows) {
		log.Fatal("active key not found")
	}
	fatal(err)
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,action,target_type,target_id,metadata) VALUES($1,'revoke_api_key','api_key',$2,jsonb_build_object('prefix',$3::text))`, *by, keyID, *prefix)
	fatal(err)
	fatal(tx.Commit(ctx))
	fmt.Println("ok")
}

func normalizeClaim(kind, value string) string {
	value = strings.TrimSpace(value)
	if kind == "git_remote" {
		value = strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(value, "/"), ".git"), "/"))
	}
	if kind == "local_path" {
		value = strings.TrimSuffix(value, "/")
	}
	return value
}
func requireOwner(ctx context.Context, tx pgx.Tx, principal string) {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals p JOIN space_grants g ON g.principal_id=p.id WHERE p.id=$1 AND p.disabled_at IS NULL AND g.role='owner')`, principal).Scan(&allowed)
	fatal(err)
	if !allowed {
		log.Fatal("--by Principal must be an active Space Owner")
	}
}
func required(name string) string {
	value := os.Getenv(name)
	if value == "" {
		log.Fatalf("%s is required", name)
	}
	return value
}
func fatal(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
func usage() {
	fmt.Fprintln(os.Stderr, "usage: memoryctl <bootstrap|create-principal|create-space|grant|bind-workspace|issue-key|revoke-key> [flags]")
	os.Exit(2)
}
