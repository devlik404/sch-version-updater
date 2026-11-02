IF OBJECT_ID('dbo.VersionUpdateJob', 'U') IS NULL
BEGIN
    CREATE TABLE dbo.VersionUpdateJob (
        id BIGINT IDENTITY(1,1) PRIMARY KEY,
        product_id VARCHAR(50) NOT NULL,
        product_name VARCHAR(150) NOT NULL,
        job_name VARCHAR(150) NOT NULL,
        job_type VARCHAR(50) NOT NULL,
        repo_url VARCHAR(500) NOT NULL,
        branch_name VARCHAR(150) NOT NULL,
        file_path VARCHAR(500) NOT NULL,
        target_type VARCHAR(30) NOT NULL,
        image_name VARCHAR(300) NULL,
        current_version VARCHAR(100) NULL,
        target_version VARCHAR(100) NULL,
        is_active BIT NOT NULL CONSTRAINT DF_VersionUpdateJob_IsActive DEFAULT 1,
        flag_done TINYINT NOT NULL CONSTRAINT DF_VersionUpdateJob_FlagDone DEFAULT 0,
        job_status VARCHAR(20) NOT NULL CONSTRAINT DF_VersionUpdateJob_Status DEFAULT 'PENDING',
        created_at DATETIME2 NOT NULL CONSTRAINT DF_VersionUpdateJob_CreatedAt DEFAULT SYSDATETIME(),
        last_run_at DATETIME2 NULL,
        CONSTRAINT CK_VersionUpdateJob_FlagDone CHECK (flag_done IN (0,1,2,3)),
        CONSTRAINT CK_VersionUpdateJob_Status CHECK (job_status IN ('PENDING','RUNNING','FAILED','SUCCESS'))
    );
END;
