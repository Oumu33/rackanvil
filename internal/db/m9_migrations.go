// M9: vendor-schema alignment migrations.
//
// The vendor server's embedded migrations define 71 tables; our M1-M6
// schema covered 16 of them (+hosts) with clean-room designs. The tables
// below restore the remaining vendor tables field-for-field (source:
// CREATE TABLE statements extracted from the vendor binary, see
// deplos-reversing/m9-evidence/db/vendor_ddl.json), translated to the
// portable SQL subset used by this package:
//
//	SERIAL/BIGSERIAL -> BIGINT (IDs are application-generated, as elsewhere)
//	JSONB -> TEXT, NOW()/gen_random_uuid() defaults normalized likewise,
//	PG table partitioning dropped (single-table layout, same columns).
//
// sys_roles_v is intentionally absent: the vendor binary contains no
// CREATE TABLE/VIEW for it (zero CREATE VIEW statements), so its shape is
// not restorable from binary evidence.
package db

func init() { migrations = append(migrations, m9VendorMigrations...) }

var m9VendorMigrations = []string{
	// vendor table: asset_hardware_info
	`CREATE TABLE IF NOT EXISTS asset_hardware_info (
    id BIGINT PRIMARY KEY,
    sn VARCHAR(100) UNIQUE NOT NULL REFERENCES hosts(sn) ON DELETE CASCADE,
    client_ip VARCHAR(50),                
    collected_at TIMESTAMP,               
    hardware_profile TEXT,
    -- 【新增】把这个字段直接写进建表语句里
    hardware_health TEXT DEFAULT '{}',               
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
)`,
	// vendor table: asset_mac_relations
	`CREATE TABLE IF NOT EXISTS asset_mac_relations (
    mac_address VARCHAR(50) NOT NULL PRIMARY KEY,
    sn          VARCHAR(100) NOT NULL,
    updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
)`,
	// vendor table: asset_user_grants
	`CREATE TABLE IF NOT EXISTS asset_user_grants (
			id                 BIGINT PRIMARY KEY,
			user_id            INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			asset_sn           VARCHAR(100) NOT NULL,
			permission         VARCHAR(50) NOT NULL,
			granted            BOOLEAN DEFAULT 1,
			requires_approval  BOOLEAN DEFAULT 0,
			created_by         INT REFERENCES users(id),
			created_at         TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(user_id, asset_sn, permission)
		)`,
	// vendor table: audit_recovery_log
	`CREATE TABLE IF NOT EXISTS audit_recovery_log (
    id BIGINT PRIMARY KEY,
    sn VARCHAR(100) NOT NULL,
    action VARCHAR(50) NOT NULL, 
    triggered_by VARCHAR(50),    
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
)`,
	// vendor table: backup_images
	`CREATE TABLE IF NOT EXISTS backup_images (
				id VARCHAR(50) PRIMARY KEY,           -- 镜像唯一ID
				name VARCHAR(255) NOT NULL,           -- 镜像显示名称 (如: Win10-研发基线)
				os_type VARCHAR(50),                  -- 操作系统类型 (Windows, Linux)
				fs_type VARCHAR(50),                  -- 文件系统类型 (ntfs, ext4, xfs)
				storage_path TEXT NOT NULL,           -- 服务端物理存储路径 (如: /pxedata/images/win10_base)
				size_bytes BIGINT DEFAULT 0,          -- 镜像大小
				description TEXT,                     -- 备注信息
				created_by VARCHAR(50) DEFAULT 'system',
				created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
			)`,
	// vendor table: backup_tasks
	`CREATE TABLE IF NOT EXISTS backup_tasks (
				task_id VARCHAR(50) PRIMARY KEY,      -- 任务ID
				sn VARCHAR(100) NOT NULL,             -- 关联主机的 SN
				asset_id VARCHAR(50),                 -- 关联的资产 ID
				image_id VARCHAR(50) REFERENCES backup_images(id) ON DELETE SET NULL,
				task_type VARCHAR(20) NOT NULL,       -- backup, restore_unicast, restore_multicast
				status VARCHAR(30) DEFAULT 'Pending', -- Pending, Booting, Running, Success, Failed
				progress INT DEFAULT 0,               -- 任务进度 (0-100)
				speed VARCHAR(50) DEFAULT '0 MB/s',   -- 实时速度
				client_ip VARCHAR(50),                -- 执行任务时的客户端IP
				log_summary TEXT,                     -- 任务日志摘要/报错信息
				started_at TIMESTAMP,
				completed_at TIMESTAMP,
				created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
			)`,
	// vendor table: batch_members
	`CREATE TABLE IF NOT EXISTS batch_members (
				task_id VARCHAR(50) PRIMARY KEY,  -- 针对每一台 Agent 生成的唯一 TaskID
				batch_id VARCHAR(50) REFERENCES batch_tasks(id),
				sn VARCHAR(100) NOT NULL,         -- 授权的设备 SN
				status VARCHAR(20) DEFAULT 'Pending', 
				progress FLOAT DEFAULT 0,
				speed VARCHAR(50),
				log_summary TEXT,
				updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
			)`,
	// vendor table: batch_tasks
	`CREATE TABLE IF NOT EXISTS batch_tasks (
				id VARCHAR(50) PRIMARY KEY,       -- 批次 ID (如 BTH-17000000)
				image_id VARCHAR(50) NOT NULL,
				source_disk VARCHAR(50) NOT NULL, -- 目标物理磁盘 (如 /dev/sda)
				storage_config TEXT NOT NULL,    -- 存储驱动与凭据
				created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
			)`,
	// vendor table: bios_job_assets
	`CREATE TABLE IF NOT EXISTS bios_job_assets (
			job_id BIGINT REFERENCES bios_jobs(id) ON DELETE CASCADE,
			sn VARCHAR(100),
			status VARCHAR(30) DEFAULT 'pending',
			step_status TEXT,
			retry_count INT DEFAULT 0,
			bios_etag VARCHAR(256),
			bios_diff TEXT,
			bios_drift_report TEXT,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (job_id, sn)
		)`,
	// vendor table: bios_jobs
	`CREATE TABLE IF NOT EXISTS bios_jobs (
			id BIGINT PRIMARY KEY,
			status VARCHAR(30) DEFAULT 'pending',
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			completed_at TIMESTAMP
		)`,
	// vendor table: bios_templates
	`CREATE TABLE IF NOT EXISTS bios_templates (
			id               BIGINT PRIMARY KEY,
			fingerprint_hash VARCHAR(64)  NOT NULL UNIQUE,
			vendor           VARCHAR(128) NOT NULL DEFAULT '',
			model            VARCHAR(256) NOT NULL DEFAULT '',
			fw_version       VARCHAR(128) NOT NULL DEFAULT '',
			tags             TEXT        NOT NULL DEFAULT '[]',
			registry_schema  TEXT        NOT NULL DEFAULT '{}',
			fleet_count      INT          NOT NULL DEFAULT 0,
			created_at       TIMESTAMP  NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at       TIMESTAMP  NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
	// vendor table: collected_logs
	`CREATE TABLE IF NOT EXISTS collected_logs (
    id BIGINT,
    sn VARCHAR(100) NOT NULL,
    ip VARCHAR(50) NOT NULL,
    log_type VARCHAR(50) NOT NULL,
    filename VARCHAR(255) NOT NULL,
    content TEXT, -- 存储日志内容
    file_size BIGINT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id, created_at)
)`,
	// vendor table: component_history
	`CREATE TABLE IF NOT EXISTS component_history (
    id BIGINT PRIMARY KEY,
    asset_sn VARCHAR(100) NOT NULL REFERENCES hosts(sn) ON DELETE CASCADE,
    component_type VARCHAR(50) NOT NULL,
    slot_location VARCHAR(100) NOT NULL,
    action_type VARCHAR(100) NOT NULL,
    old_value TEXT,
    new_value TEXT,
    changed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
)`,
	// vendor table: config_tasks
	`CREATE TABLE IF NOT EXISTS config_tasks (
			task_id BIGINT PRIMARY KEY,
			target_ip VARCHAR(50) NOT NULL,
			task_type VARCHAR(50) NOT NULL, 
			payload TEXT NOT NULL,
			status VARCHAR(50) DEFAULT 'Pending',
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
	// vendor table: cpu_tasks_reports
	`CREATE TABLE IF NOT EXISTS cpu_tasks_reports (
					task_id VARCHAR(100) PRIMARY KEY,
					sn VARCHAR(100) NOT NULL,
					asset_id VARCHAR(100),
					report_json TEXT,
					report_html TEXT,
					test_time TIMESTAMP DEFAULT CURRENT_TIMESTAMP
				)`,
	// vendor table: cpu_test_reports
	`CREATE TABLE IF NOT EXISTS cpu_test_reports (
            sn VARCHAR(100) PRIMARY KEY,
            asset_id VARCHAR(50),
            client_ip VARCHAR(50),
            report_json TEXT,
            report_html TEXT,
            test_time TIMESTAMP,
            updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
        )`,
	// vendor table: custom_scripts
	`CREATE TABLE IF NOT EXISTS custom_scripts (
			id          TEXT PRIMARY KEY,
			name        TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			type        TEXT NOT NULL DEFAULT '',
			created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
	// vendor table: discovered_hosts
	`CREATE TABLE IF NOT EXISTS discovered_hosts (
    id BIGINT PRIMARY KEY,
    mac VARCHAR(50) UNIQUE NOT NULL, 
    sn VARCHAR(100),                 
    ip VARCHAR(50),                  
    hardware_summary TEXT,          
    first_seen TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    last_seen TIMESTAMP DEFAULT CURRENT_TIMESTAMP
)`,
	// vendor table: disk_single_reports
	`CREATE TABLE IF NOT EXISTS disk_single_reports (
    sn VARCHAR(100),             -- 服务器 SN
    device_name VARCHAR(50),     -- 盘符 (如 /dev/sda)
    asset_id VARCHAR(50),        -- 资产编号 (用于双重校验)
    client_ip VARCHAR(50),
    report_json TEXT,           -- 单盘详细数据
    report_html TEXT,            -- 单盘 HTML 报告
    test_time TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (sn, device_name) 
)`,
	// vendor table: disk_single_tasks_reports
	`CREATE TABLE IF NOT EXISTS disk_single_tasks_reports (
			task_id VARCHAR(50) NOT NULL,
			device_name VARCHAR(50) NOT NULL,
			sn VARCHAR(100) NOT NULL,
			asset_id VARCHAR(50),
			client_ip VARCHAR(50),
			report_json TEXT,
			report_html TEXT,
			test_time TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (task_id, device_name)
		)`,
	// vendor table: disk_tasks_reports
	`CREATE TABLE IF NOT EXISTS disk_tasks_reports (
			task_id VARCHAR(50) PRIMARY KEY,
			sn VARCHAR(100) NOT NULL,
			asset_id VARCHAR(50),
			client_ip VARCHAR(50),
			report_json TEXT,
			report_html TEXT,
			test_time TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
	// vendor table: disk_test_reports
	`CREATE TABLE IF NOT EXISTS disk_test_reports (
    sn VARCHAR(100) PRIMARY KEY, 
    asset_id VARCHAR(50),
    client_ip VARCHAR(50),
    report_json TEXT,           
    report_html TEXT,            
    test_time TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
)`,
	// vendor table: gpu_test_reports
	`CREATE TABLE IF NOT EXISTS gpu_test_reports (
        sn VARCHAR(100) PRIMARY KEY,
        asset_id VARCHAR(50),
        client_ip VARCHAR(50),
        report_json TEXT,
        report_html TEXT,
        test_time TIMESTAMP,
        updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
    )`,
	// vendor table: gpustress_reports
	`CREATE TABLE IF NOT EXISTS gpustress_reports (
    sn VARCHAR(100) PRIMARY KEY,
    asset_id VARCHAR(50),
    client_ip VARCHAR(50),
    test_type VARCHAR(20) DEFAULT 'burn',
    report_json TEXT,       -- 存储详细的 metrics 和 analysis
    report_html TEXT,        -- 存储渲染好的 HTML
    test_time TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
)`,
	// vendor table: hardware_events
	`CREATE TABLE IF NOT EXISTS hardware_events (
    sn VARCHAR(100) NOT NULL,
    record_id VARCHAR(50) NOT NULL,
    event_time TIMESTAMP NOT NULL,
    sensor_type VARCHAR(50),
    event_type VARCHAR(100),
    raw_message TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (sn, record_id, event_time)
)`,
	// vendor table: hardware_logs
	`CREATE TABLE IF NOT EXISTS hardware_logs (
			id              BIGINT PRIMARY KEY,
			sn              VARCHAR(100) NOT NULL,    -- 服务器序列号
			asset_id        VARCHAR(50),             -- 资产编号
			log_type        VARCHAR(32) NOT NULL,    -- sel, raid, smart, gpu, nic, optical, system, mcelog
			sub_index       VARCHAR(128),            -- 子索引: 硬盘SN、网卡接口名、GPU ID等
			
			-- 分析结论
			is_fault        BOOLEAN DEFAULT 0,   -- 是否存在故障
			severity        VARCHAR(16),             -- Critical, Warning, Info
			fault_summary   TEXT,                    -- 规则引擎给出的解析结论
			
			-- 存储引用
			file_path       TEXT NOT NULL,           -- 在 /pxedata/hwlogs/ 下的相对路径
			
			-- 关联元数据
			task_id         VARCHAR(50),             -- 关联的压测/采集任务ID
			created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
	// vendor table: host_status
	`CREATE TABLE IF NOT EXISTS host_status (
    host_id INT PRIMARY KEY REFERENCES hosts(id) ON DELETE CASCADE,
    status VARCHAR(50) DEFAULT 'Offline', 
    status_msg TEXT,
    os_ip VARCHAR(50),
    last_seen TIMESTAMP,
    cpu_info TEXT,
    mem_info TEXT,
    disk_info TEXT,
    net_info TEXT,
    gpu_info TEXT,
    env_info TEXT, 
    ipmi_info TEXT, 
    alert_state TEXT DEFAULT '{}',
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
)`,
	// vendor table: identity_registry
	`CREATE TABLE IF NOT EXISTS identity_registry (
        identifier    VARCHAR(200) NOT NULL PRIMARY KEY,
        asset_id      VARCHAR(50)  NOT NULL,
        id_type       VARCHAR(20)  NOT NULL,
        registered_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
    )`,
	// vendor table: install_tasks
	`CREATE TABLE IF NOT EXISTS install_tasks (
    asset_id VARCHAR(50) PRIMARY KEY,
    status VARCHAR(100) NOT NULL DEFAULT 'PENDING', 
    task_config TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
)`,
	// vendor table: ip_pool
	`CREATE TABLE IF NOT EXISTS ip_pool (
    ip_address TEXT PRIMARY KEY,
    ip_info_json TEXT NOT NULL
)`,
	// vendor table: ipmi_audit_log
	`CREATE TABLE IF NOT EXISTS ipmi_audit_log (
        id BIGINT PRIMARY KEY,
        asset_id VARCHAR(50),
        sn VARCHAR(100),
        ipmi_ip VARCHAR(50),
        action VARCHAR(50) NOT NULL,
        detail TEXT,
        operator VARCHAR(100) DEFAULT 'system',
        source VARCHAR(50) DEFAULT 'system',
        created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
    )`,
	// vendor table: ipmi_probe_tasks
	`CREATE TABLE IF NOT EXISTS ipmi_probe_tasks (
        id BIGINT PRIMARY KEY,
        task_id TEXT UNIQUE NOT NULL ,
        asset_id VARCHAR(50) NOT NULL,
        sn VARCHAR(100),
        status VARCHAR(32) NOT NULL DEFAULT 'pending',
        stage VARCHAR(50) DEFAULT '',
        message TEXT DEFAULT '',
        ipmi_ip VARCHAR(50),
        ipmi_user VARCHAR(50),
        ipmi_pass_encrypted TEXT DEFAULT '',
        probe_source VARCHAR(20) DEFAULT 'auto',
        operator VARCHAR(100) DEFAULT 'system',
        version INT NOT NULL DEFAULT 0,
        cooldown_until TIMESTAMP,
        started_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
        finished_at TIMESTAMP,
        updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
    )`,
	// vendor table: job_assets
	`CREATE TABLE IF NOT EXISTS job_assets (
			job_id VARCHAR(50) REFERENCES orchestration_jobs(id) ON DELETE CASCADE,
			discovered_id INTEGER REFERENCES discovered_hosts(id) ON DELETE SET NULL,
			asset_number VARCHAR(50) NOT NULL,
			sn VARCHAR(100),
			status VARCHAR(30) DEFAULT 'pending',
			current_step VARCHAR(50),
			step_status TEXT DEFAULT '{}',
			result TEXT DEFAULT '{}',
			error_msg TEXT,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (job_id, discovered_id)
		)`,
	// vendor table: job_templates
	`CREATE TABLE IF NOT EXISTS job_templates (
			id BIGINT PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			steps_config TEXT DEFAULT '{}',
			job_configs TEXT DEFAULT '{}',
			network_rules TEXT DEFAULT '[]',
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
	// vendor table: memory_tasks_reports
	`CREATE TABLE IF NOT EXISTS memory_tasks_reports (
    task_id VARCHAR(100) PRIMARY KEY,
    sn VARCHAR(100) NOT NULL,
    asset_id VARCHAR(100),
    report_json TEXT,
    report_html TEXT,
    test_time TIMESTAMP DEFAULT CURRENT_TIMESTAMP
)`,
	// vendor table: memory_test_reports
	`CREATE TABLE IF NOT EXISTS memory_test_reports (
    sn VARCHAR(100) PRIMARY KEY, -- 以 SN 为主键，确保每个机器只有一份最新报告
    asset_id VARCHAR(50),
    client_ip VARCHAR(50),
    report_json TEXT,           -- 存储完整的结构化数据 (MemoryTestState)
    report_html TEXT,            -- 存储生成的 HTML 字符串 (直接用于前端展示/下载)
    test_time TIMESTAMP,         -- 压测完成时间
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
)`,
	// vendor table: mirror_local
	`CREATE TABLE IF NOT EXISTS mirror_local (
    -- 主键：nanoid 格式，由应用层生成
    id                  TEXT        NOT NULL PRIMARY KEY,

    -- 源名称：用户自定义，展示用
    name                TEXT        NOT NULL,

    -- 关联的 ISO 资产 ID（对应 ipxeinstall 模块的 os_assets.id）
    -- 注意：不设置外键约束，因为跨模块引用，由应用层保证一致性
    os_asset_id         TEXT        NOT NULL,

    -- OS 家族：rhel / debian / unknown
    -- 用于决定生成 .repo 还是 sources.list 格式的配置脚本
    os_family           TEXT        NOT NULL DEFAULT 'unknown',

    -- OS 主版本号：7 / 8 / 9 / 20 / 22 等
    -- 用于区分 RHEL 7/8+ 的 repo 结构差异，以及 Debian 发行代号推断
    major_version       INTEGER     NOT NULL DEFAULT 0,

    -- 对外 HTTP 访问路径（相对路径）
    -- 格式：/unpacked/{os_asset_id}
    -- 复用 ipxeinstall 已挂载并暴露的路径，不重复挂载
    access_path         TEXT        NOT NULL,

    -- 状态：active / offline
    -- active:  挂载点目录存在，可正常访问
    -- offline: 挂载点目录不存在（ISO 已被卸载或资产已删除）
    status              TEXT        NOT NULL DEFAULT 'active'
                            CHECK (status IN ('active', 'offline')),

    -- 描述信息（可选）
    description         TEXT        NOT NULL DEFAULT '',

    -- 创建人（预留字段，当前固定为 system）
    created_by          TEXT        NOT NULL DEFAULT 'system',

    -- 时间字段
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
)`,
	// vendor table: mirror_remote
	`CREATE TABLE IF NOT EXISTS mirror_remote (
    -- 主键：nanoid 格式，由应用层生成
    id                      TEXT        NOT NULL PRIMARY KEY,

    -- 源名称：用户自定义，展示用
    name                    TEXT        NOT NULL,

    -- 上游镜像站地址
    -- 支持协议：rsync:// / http:// / https:// / ftp://
    upstream_url            TEXT        NOT NULL,

    -- 本地存储目录（绝对路径）
    -- 严格限制在 /pxedata/mirror/ 下
    -- 格式：/pxedata/mirror/{sanitized_name}
    local_dir               TEXT        NOT NULL,

    -- OS 家族：rhel / debian / unknown
    os_family               TEXT        NOT NULL DEFAULT 'unknown',

    -- 同步协议：rsync / reposync / wget
    sync_protocol           TEXT        NOT NULL DEFAULT 'rsync'
                                CHECK (sync_protocol IN ('rsync', 'reposync', 'wget')),

    -- Cron 定时同步表达式（可为空，表示不启用定时同步）
    -- 支持标准 5 位 Cron 和带秒的 6 位 Cron
    -- 示例：0 2 * * *（每天凌晨 2 点）
    sync_schedule           TEXT        NOT NULL DEFAULT '',

    -- 当前同步状态：idle / syncing / success / failed
    -- idle:    空闲，从未同步或已手动重置
    -- syncing: 正在同步中
    -- success: 上次同步成功
    -- failed:  上次同步失败
    sync_status             TEXT        NOT NULL DEFAULT 'idle'
                                CHECK (sync_status IN ('idle', 'syncing', 'success', 'failed')),

    -- 上次同步时间（NULL 表示从未同步）
    last_sync_time          TIMESTAMP,

    -- 下次计划同步时间（由 Cron 调度器计算后写入，NULL 表示未配置定时）
    next_sync_time          TIMESTAMP,

    -- 上次同步的错误日志（同步成功时清空）
    -- 存储最后 50 行输出，用于问题排查
    last_error_log          TEXT        NOT NULL DEFAULT '',

    -- 本地存储占用字节数（同步成功后异步计算更新）
    storage_usage_bytes     BIGINT      NOT NULL DEFAULT 0,

    -- 创建人（预留字段，当前固定为 system）
    created_by              TEXT        NOT NULL DEFAULT 'system',

    -- 时间字段
    created_at              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
)`,
	// vendor table: mirror_sync_history
	`CREATE TABLE IF NOT EXISTS mirror_sync_history (
    -- 主键：自增 ID（历史记录不需要 nanoid）
    id                  BIGINT   NOT NULL PRIMARY KEY,

    -- 关联的远程同步源 ID
    -- 不设外键约束，远程源删除后历史记录保留（用于审计）
    remote_id           TEXT        NOT NULL,

    -- 远程源名称快照（防止源被删除后历史记录失去上下文）
    remote_name         TEXT        NOT NULL,

    -- 触发方式：manual（手动触发）/ cron（定时触发）
    trigger_type        TEXT        NOT NULL DEFAULT 'manual'
                            CHECK (trigger_type IN ('manual', 'cron')),

    -- 同步结果：success / failed
    result              TEXT        NOT NULL
                            CHECK (result IN ('success', 'failed')),

    -- 同步开始时间
    started_at          TIMESTAMP NOT NULL,

    -- 同步结束时间
    finished_at         TIMESTAMP NOT NULL,

    -- 同步耗时（秒）
    duration_seconds    INTEGER     NOT NULL DEFAULT 0,

    -- 同步前存储占用（字节）
    size_before_bytes   BIGINT      NOT NULL DEFAULT 0,

    -- 同步后存储占用（字节）
    size_after_bytes    BIGINT      NOT NULL DEFAULT 0,

    -- 错误信息（成功时为空）
    error_message       TEXT        NOT NULL DEFAULT '',

    -- 记录创建时间
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
)`,
	// vendor table: monitor_history
	`CREATE TABLE IF NOT EXISTS monitor_history (
    id BIGINT, 
    sn VARCHAR(100) NOT NULL,
    report_type VARCHAR(50),
    raw_data TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id, created_at)
)`,
	// vendor table: netperf_reports
	`CREATE TABLE IF NOT EXISTS netperf_reports (
        sn VARCHAR(100) PRIMARY KEY,
        asset_id VARCHAR(50),
        task_id VARCHAR(50),
        client_ip VARCHAR(50),
        report_json TEXT,
        report_html TEXT,
        test_time TIMESTAMP,
        updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
    )`,
	// vendor table: network_config_tasks
	`CREATE TABLE IF NOT EXISTS network_config_tasks (
        id BIGINT PRIMARY KEY,
        switch_ip VARCHAR(50) NOT NULL REFERENCES switches(ip) ON DELETE CASCADE,
        task_type VARCHAR(50) NOT NULL, -- e.g., 'SET_VLAN', 'DESC_PORT', 'STATIC_ARP'
        payload TEXT NOT NULL,         -- 存储具体参数 {port: "G1/0/1", vlan: 10}
        status VARCHAR(20) DEFAULT 'Pending', -- Pending, Running, Success, Failed
        result_log TEXT,                -- 存储 SSH 交互的完整回显
        created_by VARCHAR(50),
        created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
        updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
    )`,
	// vendor table: notification_config
	`CREATE TABLE IF NOT EXISTS notification_config (
    id BIGINT PRIMARY KEY,
    dingtalk_token VARCHAR(255) DEFAULT '',
    wechat_key VARCHAR(255) DEFAULT '', -- 新环境会直接创建此字段
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
)`,
	// vendor table: nvme_passthrough_blacklist
	`CREATE TABLE IF NOT EXISTS nvme_passthrough_blacklist (
			vendor_id   VARCHAR(8)  NOT NULL,
			device_id   VARCHAR(8)  NOT NULL,
			error_msg   TEXT,
			created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (vendor_id, device_id)
		)`,
	// vendor table: nvme_passthrough_whitelist
	`CREATE TABLE IF NOT EXISTS nvme_passthrough_whitelist (
			vendor_id   VARCHAR(8)  NOT NULL,
			device_id   VARCHAR(8)  NOT NULL,
			device_name TEXT,
			created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (vendor_id, device_id)
		)`,
	// vendor table: orchestration_jobs
	`CREATE TABLE IF NOT EXISTS orchestration_jobs (
    id VARCHAR(50) PRIMARY KEY,
    name VARCHAR(255),
    job_type VARCHAR(20),       -- 'new_server', 'existing_server'
    source_type VARCHAR(20),    -- 'auto_report', 'scan'
    source_ids TEXT,           -- discovered_hosts.id 数组
    asset_number_mapping TEXT, -- {"1": "DC01-001"} mapping
    steps_config TEXT,         -- 步骤开关 {"bmc_check": true, "os_install": false}
    status VARCHAR(30) DEFAULT 'pending', -- pending, running, success, failed
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMP,
    completed_at TIMESTAMP
)`,
	// vendor table: org_nodes
	`CREATE TABLE IF NOT EXISTS org_nodes (
			id          BIGINT PRIMARY KEY,
			parent_id   INT REFERENCES org_nodes(id) ON DELETE CASCADE,
			node_type   VARCHAR(20) NOT NULL DEFAULT 'group',
			name        VARCHAR(100) NOT NULL,
			code        VARCHAR(50) UNIQUE NOT NULL,
			path        VARCHAR(500) NOT NULL DEFAULT '',
			level       INT NOT NULL DEFAULT 0,
			description TEXT DEFAULT '',
			sort_order  INT DEFAULT 0,
			created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
	// vendor table: os_assets
	`CREATE TABLE IF NOT EXISTS os_assets (
    id VARCHAR(100) PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    family VARCHAR(50),
    asset_json TEXT NOT NULL, -- 存储完整的 OSAsset 结构体
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
)`,
	// vendor table: raid_operation_logs
	`CREATE TABLE IF NOT EXISTS raid_operation_logs (
        id          BIGINT PRIMARY KEY,
        sn          VARCHAR(64)  NOT NULL,         -- 设备 SN
        asset_id    VARCHAR(64)  DEFAULT '',       -- 资产编号
        task_id     VARCHAR(64)  NOT NULL,         -- 关联的任务ID
        action      VARCHAR(64)  NOT NULL,         -- 动作名称 (info, create, delete_vd, 等)
        params      TEXT        DEFAULT '{}',  -- JSON 格式的参数快照
        operator    VARCHAR(128) DEFAULT 'system', -- 操作员
        result      VARCHAR(20)  DEFAULT 'Running', -- Running, Success, Failed
        message     TEXT         DEFAULT '',      -- 结果详情或错误信息
        started_at  TIMESTAMP  NOT NULL DEFAULT CURRENT_TIMESTAMP,
        finished_at TIMESTAMP
    )`,
	// vendor table: resource_groups
	`CREATE TABLE IF NOT EXISTS resource_groups (
			id BIGINT PRIMARY KEY,
			name VARCHAR(100) UNIQUE NOT NULL,
			description TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
	// vendor table: role_permissions
	`CREATE TABLE IF NOT EXISTS role_permissions (
			role_id       INT NOT NULL REFERENCES sys_roles_v2(id) ON DELETE CASCADE,
			permission_id INT NOT NULL REFERENCES sys_permissions(id) ON DELETE CASCADE,
			PRIMARY KEY (role_id, permission_id)
		)`,
	// vendor table: stress_tasks
	`CREATE TABLE IF NOT EXISTS stress_tasks (
    task_id VARCHAR(50) PRIMARY KEY,
    sn VARCHAR(100) NOT NULL,
    asset_id VARCHAR(50),
    task_type VARCHAR(30) NOT NULL, -- 'cpu', 'memory', 'disk', etc.
    status VARCHAR(30) DEFAULT 'Pending', -- Pending, Booting, Ready, Running, Completed, Failed, Archived
    client_ip VARCHAR(50),
    message TEXT,
    task_config TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
)`,
	// vendor table: switch_arp_table
	`CREATE TABLE IF NOT EXISTS switch_arp_table (
			switch_ip VARCHAR(50) NOT NULL REFERENCES switches(ip) ON DELETE CASCADE,
			ip_address VARCHAR(50) NOT NULL,
			mac_address VARCHAR(50) NOT NULL,
			port_name VARCHAR(100),
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY(switch_ip, ip_address)
		)`,
	// vendor table: switch_lldp_table
	`CREATE TABLE IF NOT EXISTS switch_lldp_table (
        local_sw_ip VARCHAR(50) NOT NULL,
        local_port VARCHAR(100),
        remote_sys_name VARCHAR(255),
        remote_port VARCHAR(100),
        remote_chassis_id VARCHAR(100), -- 邻居的 MAC 或 ID
        updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
        PRIMARY KEY(local_sw_ip, local_port, remote_chassis_id)
    )`,
	// vendor table: switch_mac_table
	`CREATE TABLE IF NOT EXISTS switch_mac_table (
			switch_ip VARCHAR(50) NOT NULL REFERENCES switches(ip) ON DELETE CASCADE,
			vlan INT,
			mac_address VARCHAR(50) NOT NULL,
			port_index INT,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY(switch_ip, vlan, mac_address)
		)`,
	// vendor table: switch_traffic_log
	`CREATE TABLE IF NOT EXISTS switch_traffic_log (
			id BIGINT,
			switch_ip VARCHAR(50) NOT NULL,
			port_index INT,
			port_name VARCHAR(100) NOT NULL,
			in_bytes BIGINT DEFAULT 0,
			out_bytes BIGINT DEFAULT 0,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (id, created_at)
		)`,
	// vendor table: switches
	`CREATE TABLE IF NOT EXISTS switches (
			id BIGINT PRIMARY KEY,
			ip VARCHAR(50) UNIQUE NOT NULL,
			snmp_version VARCHAR(10) DEFAULT 'v2c',
			snmp_community VARCHAR(100) NOT NULL,
			ssh_user VARCHAR(100),          -- [新增] 交换机管理账号
			ssh_pass VARCHAR(255),          -- [新增] 交换机管理密码
			sys_name VARCHAR(255),
			status VARCHAR(50) DEFAULT 'Online',
			last_seen TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
	// vendor table: sys_permissions
	`CREATE TABLE IF NOT EXISTS sys_permissions (
			id          BIGINT PRIMARY KEY,
			code        VARCHAR(50) UNIQUE NOT NULL,
			resource    VARCHAR(50) NOT NULL,
			action      VARCHAR(30) NOT NULL,
			name        VARCHAR(100) NOT NULL,
			description TEXT DEFAULT '',
			category    VARCHAR(50) NOT NULL,
			sort_order  INT DEFAULT 0,
			created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
	// vendor table: sys_roles
	`CREATE TABLE IF NOT EXISTS sys_roles (
			id BIGINT PRIMARY KEY,
			code VARCHAR(50) UNIQUE NOT NULL,       -- 角色编码 (如 'operator')
			name VARCHAR(100) NOT NULL,             -- 显示名称 (如 '操作员')
			permissions TEXT DEFAULT '{}',  -- 权限字典 {"hardware_test": true, "pxe_install": false}
			is_system BOOLEAN DEFAULT 0         -- 是否为系统内置(不可删除)
		)`,
	// vendor table: sys_roles_v2
	`CREATE TABLE IF NOT EXISTS sys_roles_v2 (
			id          BIGINT PRIMARY KEY,
			code        VARCHAR(50) UNIQUE NOT NULL,
			name        VARCHAR(100) NOT NULL,
			description TEXT DEFAULT '',
			data_scope  VARCHAR(20) NOT NULL DEFAULT 'group',
			is_system   BOOLEAN DEFAULT 0,
			created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
	// vendor table: trend_counters
	`CREATE TABLE IF NOT EXISTS trend_counters (
    sn VARCHAR(100) NOT NULL,
    rule_id VARCHAR(50) NOT NULL,
    window_start TIMESTAMP NOT NULL, 
    count INT DEFAULT 1,
    last_updated TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (sn, rule_id, window_start)
)`,
	// vendor table: user_roles
	`CREATE TABLE IF NOT EXISTS user_roles (
			id          BIGINT PRIMARY KEY,
			user_id     INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			role_id     INT NOT NULL REFERENCES sys_roles_v2(id) ON DELETE CASCADE,
			org_node_id INT NOT NULL REFERENCES org_nodes(id),
			granted_by  INT REFERENCES users(id),
			granted_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(user_id, role_id)
		)`,
	// vendor table: vm_hardware_info
	`CREATE TABLE IF NOT EXISTS vm_hardware_info (
    id BIGINT PRIMARY KEY,
    asset_id VARCHAR(50) UNIQUE NOT NULL, -- 关联 hosts.asset_id
    client_ip VARCHAR(50),
    os_info TEXT,
    disk_usage TEXT,
    load_info TEXT,
    collected_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
)`,
	// vendor table: vm_mac_relations
	`CREATE TABLE IF NOT EXISTS vm_mac_relations (
    mac_address VARCHAR(50) NOT NULL PRIMARY KEY,
    sn VARCHAR(100) NOT NULL, -- 对应 hosts.sn (即 VM TEXT)
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
)`,
	// vendor table: vm_shadow_list
	`CREATE TABLE IF NOT EXISTS vm_shadow_list (
    parent_sn VARCHAR(100) NOT NULL,
    vm_uuid VARCHAR(100) NOT NULL,
    vm_name VARCHAR(255),
    vm_mac VARCHAR(50),
    vcpu INT,
    mem_gb INT,
    last_seen TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (parent_sn, vm_uuid)
)`,
}
