-- Phase 7 audit C (UX): a new company starts with the apps of its business type (the server passes them; this default
-- is the generator set for any other insert) instead of every app.
-- +goose Up
ALTER TABLE companies ALTER COLUMN apps SET DEFAULT '{gen,acc,col,msg,stk,pur,hr,svc,ai}';

-- +goose Down
ALTER TABLE companies ALTER COLUMN apps SET DEFAULT '{gen,acc,col,msg,stk,pur,net,sat,pos,hr,crm,svc,mfg,prj,flt,web,mkt,ai}';
