-- PostgreSQL database dump (example fixture)
CREATE TABLE public.internal_jobs (id bigint, name text, status text);
INSERT INTO public.internal_jobs VALUES (1, 'billing_reconciliation', 'completed');
INSERT INTO public.internal_jobs VALUES (2, 'legacy_account_migration', 'queued');
