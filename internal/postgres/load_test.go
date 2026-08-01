package postgres_test

import (
	"context"
	"crypto/md5"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jahwag/zestmem/internal/memory"
	"github.com/jahwag/zestmem/internal/postgres"
)

func TestTenAgentsTenThousandMemories(t *testing.T) {
	if os.Getenv("RUN_LOAD_TEST") != "1" {
		t.Skip("RUN_LOAD_TEST is not set")
	}
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `TRUNCATE audit_events,idempotency_records,relations,memory_embeddings,memory_revisions,memories,workspace_bindings,space_grants,api_keys,spaces,principals CASCADE`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `INSERT INTO principals(id,type,display_name) VALUES(md5('load-owner')::uuid,'human','Load Owner');
INSERT INTO principals(id,type,display_name) SELECT md5('load-agent-'||n)::uuid,'agent','Agent '||n FROM generate_series(1,10)n;
INSERT INTO spaces(id,type,name,slug) SELECT md5('load-space-'||n)::uuid,'private','Space '||n,'space-'||n FROM generate_series(1,10)n;
INSERT INTO space_grants(principal_id,space_id,role,granted_by) SELECT md5('load-agent-'||n)::uuid,md5('load-space-'||n)::uuid,'contributor',md5('load-owner')::uuid FROM generate_series(1,10)n;
INSERT INTO memories(id,space_id,status,current_revision,content_hash,created_by,created_at)
 SELECT md5('load-memory-'||a||'-'||n)::uuid,md5('load-space-'||a)::uuid,'active',1,'hash-'||a||'-'||n,md5('load-agent-'||a)::uuid,now()
 FROM generate_series(1,10)a CROSS JOIN generate_series(1,1000)n;
INSERT INTO memory_revisions(memory_id,revision,title,body,kind,topics,content_hash,actor_id,reason,created_at)
 SELECT md5('load-memory-'||a||'-'||n)::uuid,1,'Database directive '||n,'Agent '||a||' should retain database setting '||n||'.','directive',ARRAY['database'],'hash-'||a||'-'||n,md5('load-agent-'||a)::uuid,'load fixture',now()
 FROM generate_series(1,10)a CROSS JOIN generate_series(1,1000)n;
INSERT INTO memory_embeddings(memory_id,revision,embedding,provider,model,model_revision,dimensions,normalization)
 SELECT md5('load-memory-'||a||'-'||n)::uuid,1,('['||array_to_string(ARRAY[1.0]||array_fill(0.0,ARRAY[383]),',')||']')::vector,
 'huggingface-tei','BAAI/bge-small-en-v1.5','5c38ec7c405ec4b44b94cc5a9bb96e735b38267a',384,'l2'
 FROM generate_series(1,10)a CROSS JOIN generate_series(1,1000)n`)
	conn.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	store, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := memory.NewService(store, store).WithEmbedder(fixedEmbedder{})
	start := time.Now()
	var wg sync.WaitGroup
	errorsCh := make(chan error, 10)
	durations := make(chan time.Duration, 100)
	for agent := 1; agent <= 10; agent++ {
		agent := agent
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				started := time.Now()
				hits, err := svc.Recall(ctx, memory.RecallQuery{Principal: memory.Principal{ID: deterministicUUID("load-agent", agent)}, Query: "database setting", Limit: 6})
				durations <- time.Since(started)
				if err != nil {
					errorsCh <- err
					return
				}
				if len(hits) != 6 {
					errorsCh <- fmt.Errorf("agent %d hits=%d", agent, len(hits))
					return
				}
				want := deterministicUUID("load-space", agent)
				for _, hit := range hits {
					if hit.SpaceID != want {
						errorsCh <- fmt.Errorf("authorization leak agent=%d space=%s want=%s", agent, hit.SpaceID, want)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	close(errorsCh)
	close(durations)
	for err := range errorsCh {
		t.Error(err)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("100 concurrent recalls took %s", elapsed)
	}
	var observed []time.Duration
	for duration := range durations {
		observed = append(observed, duration)
	}
	sort.Slice(observed, func(i, j int) bool { return observed[i] < observed[j] })
	if len(observed) == 100 {
		t.Logf("recall latency p95=%s max=%s", observed[94], observed[len(observed)-1])
	}
}

func deterministicUUID(prefix string, n int) string {
	return fmt.Sprintf("%x", md5Bytes(fmt.Sprintf("%s-%d", prefix, n)))[0:8] + "-" + fmt.Sprintf("%x", md5Bytes(fmt.Sprintf("%s-%d", prefix, n)))[8:12] + "-" + fmt.Sprintf("%x", md5Bytes(fmt.Sprintf("%s-%d", prefix, n)))[12:16] + "-" + fmt.Sprintf("%x", md5Bytes(fmt.Sprintf("%s-%d", prefix, n)))[16:20] + "-" + fmt.Sprintf("%x", md5Bytes(fmt.Sprintf("%s-%d", prefix, n)))[20:32]
}
func md5Bytes(value string) []byte { sum := md5.Sum([]byte(value)); return sum[:] }
