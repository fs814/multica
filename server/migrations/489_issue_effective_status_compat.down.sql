DO $do$
DECLARE
    v_description text;
BEGIN
    SELECT obj_description(to_regprocedure('public.issue_effective_status(uuid,text)'), 'pg_proc')
      INTO v_description;
    IF v_description = 'tes66-migration-489-compatibility-helper' THEN
        DROP FUNCTION public.issue_effective_status(uuid, text);
    END IF;
END
$do$;
