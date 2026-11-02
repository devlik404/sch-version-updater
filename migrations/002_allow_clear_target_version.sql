-- Required for databases created with target_version NOT NULL.
IF OBJECT_ID('dbo.VersionUpdateJob', 'U') IS NOT NULL
BEGIN
    ALTER TABLE dbo.VersionUpdateJob
        ALTER COLUMN target_version VARCHAR(100) NULL;
END;
