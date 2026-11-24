ALTER TABLE business_applications ADD COLUMN IF NOT EXISTS inn text NOT NULL DEFAULT '';
ALTER TABLE business_applications ADD COLUMN IF NOT EXISTS ogrn text NOT NULL DEFAULT '';
ALTER TABLE business_applications ADD COLUMN IF NOT EXISTS address text NOT NULL DEFAULT '';
ALTER TABLE business_applications ADD COLUMN IF NOT EXISTS contact_person text NOT NULL DEFAULT '';
ALTER TABLE business_applications ADD COLUMN IF NOT EXISTS bik text NOT NULL DEFAULT '';
ALTER TABLE business_applications ADD COLUMN IF NOT EXISTS account text NOT NULL DEFAULT '';

ALTER TABLE business_offers ADD COLUMN IF NOT EXISTS schedule text NOT NULL DEFAULT '';
ALTER TABLE business_offers ADD COLUMN IF NOT EXISTS photo_path text NOT NULL DEFAULT '';
