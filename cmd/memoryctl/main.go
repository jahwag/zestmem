package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jahwag/zestmem/internal/authn"
	"github.com/jahwag/zestmem/internal/buildinfo"
	"github.com/jahwag/zestmem/internal/memory"
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
	case "set-space-use-when":
		setSpaceUseWhen(ctx, conn, os.Args[2:])
	case "disable-principal":
		disablePrincipal(ctx, conn, os.Args[2:])
	default:
		usage()
	}
}

func setSpaceUseWhen(ctx context.Context, conn *pgx.Conn, args []string) {
	fs := flag.NewFlagSet("set-space-use-when", flag.ExitOnError)
	space := fs.String("space", "", "Space UUID")
	useWhen := fs.String("use-when", "", "untrusted routing cue; empty clears it")
	by := fs.String("by", "", "active owner Principal UUID")
	reason := fs.String("reason", "", "operator reason")
	fs.Parse(args)
	if *space == "" || *by == "" || strings.TrimSpace(*reason) == "" {
		log.Fatal("--space, --by, and --reason are required")
	}
	normalized, err := memory.NormalizeUseWhen(*useWhen)
	fatal(err)
	tx, err := conn.Begin(ctx)
	fatal(err)
	defer tx.Rollback(ctx)
	requireSpaceOwner(ctx, tx, *by, *space)
	var name string
	err = tx.QueryRow(ctx, `UPDATE spaces s SET use_when=NULLIF($1,'')
 WHERE s.id=$2 AND has_direct_space_role($3,s.id,'owner') RETURNING name`, normalized, *space, *by).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		log.Fatal("Space not found or --by Principal is not an active direct Owner")
	}
	fatal(err)
	hash := sha256.Sum256([]byte(normalized))
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,action,target_type,target_id,reason,metadata)
 VALUES($1,'set_space_use_when','space',$2,$3,jsonb_build_object('cue_sha256',$4::text,'cleared',$5::boolean))`,
		*by, *space, strings.TrimSpace(*reason), hex.EncodeToString(hash[:]), normalized == "")
	fatal(err)
	fatal(tx.Commit(ctx))
	fmt.Printf("space=%s\ncue_sha256=%s\n", name, hex.EncodeToString(hash[:]))
}

func disablePrincipal(ctx context.Context, conn *pgx.Conn, args []string) {
	fs := flag.NewFlagSet("disable-principal", flag.ExitOnError)
	principal := fs.String("principal", "", "agent Principal UUID")
	by := fs.String("by", "", "active owner Principal UUID")
	reason := fs.String("reason", "", "operator reason")
	fs.Parse(args)
	if *principal == "" || *by == "" || strings.TrimSpace(*reason) == "" {
		log.Fatal("--principal, --by, and --reason are required")
	}
	tx, err := conn.Begin(ctx)
	fatal(err)
	defer tx.Rollback(ctx)
	requireOwner(ctx, tx, *by)
	var displayName string
	err = tx.QueryRow(ctx, `UPDATE principals SET disabled_at=now()
 WHERE id=$1 AND type='agent' AND disabled_at IS NULL RETURNING display_name`, *principal).Scan(&displayName)
	if errors.Is(err, pgx.ErrNoRows) {
		log.Fatal("active agent Principal not found")
	}
	fatal(err)
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,action,target_type,target_id,reason)
 VALUES($1,'disable_principal','principal',$2,$3)`, *by, *principal, strings.TrimSpace(*reason))
	fatal(err)
	fatal(tx.Commit(ctx))
	fmt.Printf("disabled=%s\n", displayName)
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
	secret, err := authn.GenerateAPIKey()
	fatal(err)
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
	err := tx.QueryRow(ctx, `SELECT true FROM principals p JOIN space_grants g ON g.principal_id=p.id
 WHERE p.id=$1 AND p.disabled_at IS NULL AND g.role='owner' LIMIT 1 FOR UPDATE OF p,g`, principal).Scan(&allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		log.Fatal("--by Principal must be an active Space Owner")
	}
	fatal(err)
}

func requireSpaceOwner(ctx context.Context, tx pgx.Tx, principal, space string) {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT true FROM principals p
 JOIN space_grants g ON g.principal_id=p.id
 JOIN spaces s ON s.id=g.space_id
 WHERE p.id=$1 AND s.id=$2 AND p.disabled_at IS NULL AND s.suspended_at IS NULL AND g.role='owner'
 FOR UPDATE OF p,g,s`, principal, space).Scan(&allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		log.Fatal("--by Principal must be an active Owner of --space")
	}
	fatal(err)
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
	fmt.Fprintln(os.Stderr, "usage: memoryctl <bootstrap|create-principal|create-space|grant|bind-workspace|issue-key|revoke-key|set-space-use-when|disable-principal> [flags]")
	os.Exit(2)
}
