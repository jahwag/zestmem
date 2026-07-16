INSERT INTO principals(id,type,display_name) VALUES
('00000000-0000-0000-0000-000000000001','human','Backup Owner');
INSERT INTO spaces(id,type,name,slug) VALUES
('00000000-0000-0000-0000-000000000002','organization','Backup Org','backup-org');
INSERT INTO space_grants(principal_id,space_id,role,granted_by) VALUES
('00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000002','owner','00000000-0000-0000-0000-000000000001');
INSERT INTO memories(id,space_id,status,current_revision,content_hash,created_by,created_at) VALUES
('00000000-0000-0000-0000-000000000003','00000000-0000-0000-0000-000000000002','active',1,'fixture','00000000-0000-0000-0000-000000000001',now());
INSERT INTO memory_revisions(memory_id,revision,title,body,kind,content_hash,actor_id,reason,created_at) VALUES
('00000000-0000-0000-0000-000000000003',1,'Backup fixture','Verifies revision restoration.','fact','fixture','00000000-0000-0000-0000-000000000001','backup test',now());
INSERT INTO memory_embeddings(memory_id,revision,embedding,provider,model,model_revision,dimensions,normalization) VALUES
('00000000-0000-0000-0000-000000000003',1,('['||array_to_string(array_fill(0.0,ARRAY[384]),',')||']')::vector,
 'huggingface-tei','BAAI/bge-small-en-v1.5','5c38ec7c405ec4b44b94cc5a9bb96e735b38267a',384,'l2');
