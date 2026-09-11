ALTER TABLE issue_pool_policy ADD CONSTRAINT issue_pool_policy_pkey PRIMARY KEY USING INDEX issue_pool_policy_id_key;
ALTER TABLE issue_pool_cycle ADD CONSTRAINT issue_pool_cycle_pkey PRIMARY KEY USING INDEX issue_pool_cycle_id_key;
ALTER TABLE issue_pool_item ADD CONSTRAINT issue_pool_item_pkey PRIMARY KEY USING INDEX issue_pool_item_id_key;
