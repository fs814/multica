-- Scope changes revoke already-issued memory contexts, including A -> B -> A.
-- No relationships or dependent records are deleted by these triggers.
CREATE FUNCTION update_project_memory_epoch() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.project_id IS DISTINCT FROM NEW.project_id THEN
  INSERT INTO project_memory_scope(workspace_id,scope_kind,scope_id,project_id,epoch)
  VALUES(NEW.workspace_id,TG_TABLE_NAME,NEW.id,NEW.project_id,2)
  ON CONFLICT(workspace_id,scope_kind,scope_id) DO UPDATE
  SET project_id=EXCLUDED.project_id,epoch=project_memory_scope.epoch+1;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER issue_project_memory_epoch AFTER UPDATE OF project_id ON issue
 FOR EACH ROW EXECUTE FUNCTION update_project_memory_epoch();
CREATE TRIGGER chat_project_memory_epoch AFTER UPDATE OF project_id ON chat_session
 FOR EACH ROW EXECUTE FUNCTION update_project_memory_epoch();
