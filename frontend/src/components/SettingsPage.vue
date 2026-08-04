<template>
  <div class="page-content settings-page">
    <h2>设置</h2>

    <div class="settings-grid-shell">
    <div class="settings-section">
      <h3>基本设置</h3>
      <div class="setting-item">
        <label class="switch">
          <input type="checkbox" v-model="settingsForm.confirm_before_delete" />
          <span class="slider"></span>
          <span>删除前确认</span>
        </label>
      </div>
      <div class="setting-item">
        <label class="switch">
          <input type="checkbox" v-model="settingsForm.delete_original_file" />
          <span class="slider"></span>
          <span>默认将原始文件移入回收站</span>
        </label>
      </div>
      <div class="setting-item">
        <label class="switch">
          <input type="checkbox" v-model="settingsForm.log_enabled" />
          <span class="slider"></span>
          <span>启用日志记录</span>
        </label>
      </div>
      <div class="setting-item">
        <label>主题模式</label>
        <select v-model="settingsForm.theme" class="select-input">
          <option value="light">浅色模式 (Light)</option>
          <option value="dark">深色模式 (Dark)</option>
          <option value="system">跟随系统 (System)</option>
        </select>
      </div>
	  <div class="setting-item">
		<label>键盘快捷键</label>
		<button data-test="shortcut-help-button" type="button" class="btn-secondary" @click="showShortcutHelp = true">查看快捷键</button>
		<p class="help-text">用于列表和详情抽屉中的连续审阅。</p>
	  </div>
    </div>

    <!-- 自动化设置 -->
    <div class="settings-section">
      <h3>自动化与扫描</h3>
      <div class="setting-item">
        <label class="switch">
          <input type="checkbox" v-model="settingsForm.auto_scan_on_startup" />
          <span class="slider"></span>
          <span>启动时自动增量扫描</span>
        </label>
        <p class="help-text">开启后，每次启动应用都会自动同步磁盘文件变动并补全元数据。</p>
      </div>
      <div class="setting-item">
        <label class="switch">
          <input data-test="library-watch-toggle" type="checkbox" v-model="settingsForm.library_watch_enabled" />
          <span class="slider"></span>
          <span>实时同步片库</span>
        </label>
        <p class="help-text">监听可靠的本地或直连磁盘；网络盘仍使用启动或手动扫描。</p>
      </div>
      <div class="setting-item">
        <label class="switch">
          <input data-test="local-metadata-toggle" type="checkbox" v-model="settingsForm.local_metadata_enabled" />
          <span class="slider"></span>
          <span>本地元数据自动补全</span>
        </label>
        <p class="help-text">控制新视频自动填空与后台补全任务；详情页手工导入仍可使用。</p>
      </div>
      <div class="setting-item">
        <label class="switch">
          <input data-test="ai-quality-toggle" type="checkbox" v-model="settingsForm.ai_quality_enabled" />
          <span class="slider"></span>
          <span>显示 AI 质量评估</span>
        </label>
        <p class="help-text">只控制质量视图入口；已有归因和审核记录不会删除。</p>
      </div>
      <div class="setting-item scan-blacklist-setting">
        <div class="settings-section-heading">
          <label>扫描目录黑名单</label>
          <button type="button" class="btn-secondary btn-compact" @click="addScanExcludeDirectory">选择目录</button>
        </div>
        <div v-if="scanExcludePaths.length" class="scan-blacklist-list">
          <div v-for="path in scanExcludePaths" :key="path" class="scan-blacklist-item">
            <span :title="path">{{ path }}</span>
            <button type="button" class="btn-secondary btn-compact btn-danger-outline" @click="removeScanExcludeDirectory(path)">移除</button>
          </div>
        </div>
        <p v-else class="help-text">尚未设置黑名单目录。</p>
        <p class="help-text">黑名单目录及其全部子目录不会被后续扫描收录；已有视频记录不会自动删除。</p>
      </div>
    </div>

    <div class="settings-section">
      <h3>手机短视频</h3>
      <div class="short-feed-status">
        <div class="short-feed-status-main">
          <strong>{{ shortFeedStatusText }}</strong>
          <span v-if="shortFeedStatus && shortFeedStatus.running">{{ shortFeedStatus.url }}</span>
          <span v-else-if="shortFeedStatus && shortFeedStatus.startup_error">{{ shortFeedStatus.startup_error }}</span>
          <span v-else>短视频服务状态未知</span>
        </div>
        <div class="short-feed-actions">
          <button type="button" class="btn-secondary" @click="loadShortFeedStatus">刷新</button>
          <button
            v-if="shortFeedStatus && shortFeedStatus.running"
            type="button"
            class="btn-primary"
            @click="openShortFeed"
          >
            打开
          </button>
        </div>
      </div>
      <div v-if="shortFeedStatus && shortFeedStatus.lan_urls && shortFeedStatus.lan_urls.length" class="short-feed-lan-list">
        <div v-for="url in shortFeedStatus.lan_urls" :key="url" class="short-feed-url">{{ url }}</div>
      </div>
      <div class="setting-item short-feed-duration-setting">
        <label>短视频时长上限（分钟）</label>
        <input
          type="number"
          v-model.number="settingsForm.short_feed_max_duration_minutes"
          min="1"
          max="180"
          step="1"
          class="number-input"
        />
        <p class="help-text">只有时长小于此上限的视频会进入手机短视频，并自动维护“短视频”标签，默认 5 分钟。</p>
      </div>
	  <div class="setting-item">
		<label class="checkbox-label">
		  <input data-test="short-feed-feedback-sync-toggle" type="checkbox" v-model="settingsForm.short_feed_feedback_sync_enabled" />
		  <span>将手机端喜欢与收藏同步到主片库</span>
		</label>
		<p class="help-text">喜欢会维护“短视频喜欢”自动标签，收藏会写入主片库收藏；关闭不会删除已经同步的结果。</p>
	  </div>
      <p class="help-text">此页面仅面向本机/局域网直接访问，当前版本不启用登录或 PIN。</p>
    </div>

    <div class="settings-section">
      <h3>AI 标签</h3>
      <div class="setting-item">
        <label>接口地址</label>
        <input
          type="text"
          v-model.trim="settingsForm.ai_tagging_base_url"
          placeholder="https://api.openai.com/v1 或 http://127.0.0.1:1234/v1"
          class="text-input"
        />
      </div>
      <div class="setting-item">
        <label>API Key</label>
        <input
          type="password"
          v-model="settingsForm.ai_tagging_api_key"
          placeholder="本地 LM Studio 可留空；云端接口填写 API Key"
          class="text-input"
          autocomplete="off"
        />
      </div>
      <div class="setting-item">
        <label>模型</label>
        <input
          type="text"
          v-model.trim="settingsForm.ai_tagging_model"
          placeholder="支持图像理解的模型"
          class="text-input"
        />
      </div>
      <div class="setting-grid">
        <div class="setting-item">
          <label>单次请求图片上限</label>
          <input
            type="number"
            v-model.number="settingsForm.ai_tagging_images_per_request"
            min="1"
            max="100"
            step="1"
            class="number-input"
          />
		  <p class="help-text">视频按每分钟一帧、至少 10 帧抽取；超过此数量时拆成多个模型请求。</p>
        </div>
        <div class="setting-item">
          <label>字幕字符上限</label>
          <input
            type="number"
            v-model.number="settingsForm.ai_tagging_subtitle_char_limit"
            min="200"
            max="12000"
            step="100"
            class="number-input"
          />
        </div>
        <div class="setting-item">
          <label>后台批量数量</label>
          <input
            type="number"
            v-model.number="settingsForm.ai_tagging_startup_batch_size"
            min="1"
            max="100"
            step="1"
            class="number-input"
          />
        </div>
        <div class="setting-item">
          <label>Agent 额外补帧上限</label>
          <input
            type="number"
            v-model.number="settingsForm.ai_tagging_max_extra_frames"
            min="1"
            max="100"
            step="1"
            class="number-input"
          />
          <p class="help-text">模型决定每次补多少帧，服务端按此值限制单个视频的额外总量，默认 20。</p>
        </div>
      </div>
      <p class="help-text">AI Agent 可按证据决定补帧、临时生成字幕或查找同源视频。临时字幕只在内存中使用，不写入 SRT；只调用已经准备好的本地 WhisperX/Qwen，不会自动安装组件。</p>
      <p class="help-text">抽帧图片、临时字幕文本和同源候选帧可能发送到上方配置的外部 API；原始音频不会发送。保存后后台会自动使用新配置。</p>
      <div class="setting-item semantic-index-controls">
        <label>语义索引</label>
        <input type="text" v-model.trim="settingsForm.semantic_embedding_model" class="text-input" placeholder="例如 text-embedding-3-small；留空复用上方模型" />
        <p class="help-text">复用上方 OpenAI 兼容接口的 <code>/embeddings</code> 能力。索引仅在显式启动后构建；模型或向量维度变化时必须显式重建。</p>
        <div class="backup-status" :class="{ 'backup-status--error': semanticIndexStatus && !semanticIndexStatus.available }">
          <strong>{{ semanticIndexStatusText }}</strong>
          <span v-if="semanticIndexStatus?.model">模型 {{ semanticIndexStatus.model }}<template v-if="semanticIndexStatus.dimension"> · {{ semanticIndexStatus.dimension }} 维</template></span>
          <span v-if="semanticIndexStatus?.running || semanticIndexStatus?.completed">进度 {{ semanticIndexStatus.processed || 0 }}/{{ semanticIndexStatus.total || 0 }} · 成功 {{ semanticIndexStatus.succeeded || 0 }} · 跳过 {{ semanticIndexStatus.skipped || 0 }} · 失败 {{ semanticIndexStatus.failed || 0 }}</span>
          <span v-if="semanticIndexStatus?.unavailable">{{ semanticIndexStatus.unavailable }}</span>
        </div>
        <div class="backup-actions">
          <button type="button" class="btn-primary" :disabled="semanticIndexStatus?.running || !semanticIndexStatus?.available" @click="startSemanticIndex(false)">开始/继续构建</button>
          <button type="button" class="btn-secondary" :disabled="semanticIndexStatus?.running || !semanticIndexStatus?.available" @click="startSemanticIndex(true)">重建索引</button>
          <button v-if="semanticIndexStatus?.running" type="button" class="btn-secondary" @click="cancelSemanticIndex">取消</button>
        </div>
      </div>
    </div>

	<div class="settings-section ai-tag-library-section">
	  <div class="settings-section-heading">
		<h3>AI 标签库</h3>
		<button type="button" class="btn-secondary" @click="addAITagLibraryGroup">添加分类</button>
	  </div>
	  <p class="help-text">每个分类占一行，可在分类内维护多个标签；只有启用的标签会发送给模型。可直接填写已有普通标签的名称，保存后会保留它现有的视频关联并加入 AI 标签库。</p>
	  <div v-if="aiTagLibraryLoading" class="empty-hint">正在加载标签库...</div>
	  <div v-else-if="aiTagLibraryError" class="ai-tag-library-error">{{ aiTagLibraryError }}</div>
	  <div v-else class="ai-tag-library-list">
		<div v-for="(group, groupIndex) in localAITagGroups" :key="group._key" class="ai-tag-library-group">
		  <div class="ai-tag-library-group-heading">
			<input v-model.trim="group.namespace" type="text" class="text-input ai-tag-namespace-input" placeholder="分类名称" aria-label="标签分类名称" />
			<span class="ai-tag-count">{{ group.tags.length }} 个标签</span>
			<button type="button" class="btn-secondary btn-compact" @click="addAITagToGroup(groupIndex)">添加标签</button>
			<button type="button" class="btn-danger btn-compact" @click="removeAITagLibraryGroup(groupIndex)">删除分类</button>
		  </div>
		  <div class="ai-tag-library-group-tags">
			<div v-for="(tag, tagIndex) in group.tags" :key="tag._key" class="ai-tag-library-tag">
			  <input v-model.trim="tag.name" type="text" class="text-input" placeholder="标签名称" :aria-label="`${group.namespace || '未命名分类'}标签名称`" />
			  <input v-model="tag.color" type="color" class="ai-tag-color" aria-label="标签颜色" />
			  <label class="ai-tag-active"><input v-model="tag.is_active" type="checkbox" />启用</label>
			  <button type="button" class="btn-secondary btn-danger-outline ai-tag-remove" title="移出 AI 标签库" :aria-label="`移出标签 ${tag.name || tagIndex + 1}`" @click="removeAITagFromGroup(groupIndex, tagIndex)">×</button>
			</div>
			<div v-if="group.tags.length === 0" class="empty-hint ai-tag-group-empty">当前分类暂无标签</div>
		  </div>
		</div>
		<div v-if="localAITagGroups.length === 0" class="empty-hint">当前未配置 AI 标签，后台分析将暂停。</div>
	  </div>
	</div>

    <!-- 智能随机播放设置 -->
    <div class="settings-section">
      <h3>智能随机播放</h3>
      <div class="setting-item">
        <label>播放权重（1次普通播放 = N次随机播放）</label>
        <div class="setting-control-row">
          <input 
            type="number" 
            v-model.number="settingsForm.play_weight" 
            min="0.1" 
            max="10" 
            step="0.1"
            class="number-input"
          />
        </div>
        <p class="help-text">建议值: 1.0-3.0，默认2.0。权重越高，普通播放对随机选择的影响越大。</p>
      </div>
      <div class="setting-item">
        <label>播放记录半衰期（天）</label>
        <input type="number" v-model.number="settingsForm.random_half_life_days" min="0" max="3650" step="1" class="number-input" />
        <p class="help-text">默认 90 天；久未播放的视频会逐渐回到随机池。设为 0 时关闭衰减并完全使用原算法。</p>
      </div>
    </div>

    <!-- 视频格式设置 -->
    <div class="settings-section">
      <h3>支持的视频格式</h3>
      <div class="setting-item">
        <textarea 
          v-model="settingsForm.video_extensions" 
          rows="3"
          class="text-input settings-textarea"
          placeholder=".mp4,.avi,.mkv,.mov,.wmv,.flv,.webm,.m4v,.ts,.3gp,.mpg,.mpeg,.rm,.rmvb,.vob,.divx,.f4v,.asf,.qt"
        ></textarea>
        <p class="help-text">用逗号分隔，留空则使用默认配置。</p>
      </div>
    </div>

    <!-- 字幕设置 -->
    <div class="settings-section">
      <h3>字幕翻译</h3>
      <div class="setting-item">
        <label class="switch">
          <input type="checkbox" v-model="settingsForm.bilingual_enabled" />
          <span class="slider"></span>
          <span>启用双语字幕翻译</span>
        </label>
      </div>
      <template v-if="settingsForm.bilingual_enabled">
        <div class="setting-item">
          <label>目标翻译语言</label>
          <select v-model="settingsForm.bilingual_lang" class="select-input">
            <option value="zh">中文</option>
            <option value="en">英语</option>
            <option value="ja">日语</option>
            <option value="ko">韩语</option>
            <option value="fr">法语</option>
            <option value="de">德语</option>
            <option value="es">西班牙语</option>
            <option value="pt">葡萄牙语</option>
            <option value="ru">俄语</option>
            <option value="it">意大利语</option>
          </select>
        </div>
        <div class="setting-item">
          <label>翻译服务</label>
          <select v-model="settingsForm.subtitle_translation_provider" class="select-input">
            <option value="deepl">DeepL</option>
            <option value="llm">OpenAI 兼容接口（本地 / 远程）</option>
          </select>
        </div>
        <div v-if="settingsForm.subtitle_translation_provider !== 'llm'" class="setting-item">
          <label>DeepL API Key</label>
          <input 
            type="password" 
            v-model="settingsForm.deepl_api_key" 
            placeholder="填入 DeepL API Key" 
            class="text-input"
            autocomplete="off"
          />
          <p class="help-text">免费版 Key 通常以 :fx 结尾。额度 50 万字符/月。</p>
        </div>
        <template v-else>
          <div class="setting-item">
            <label>字幕翻译接口地址</label>
            <input type="url" v-model.trim="settingsForm.subtitle_translation_base_url" class="text-input" placeholder="https://api.example.com/v1 或 http://127.0.0.1:1234/v1" />
          </div>
          <div class="setting-item">
            <label>字幕翻译 API Key</label>
            <input type="password" v-model="settingsForm.subtitle_translation_api_key" class="text-input" autocomplete="off" />
            <p class="help-text">本地服务可留空；远程服务按提供商要求填写。此配置不会复用 AI 标签接口。</p>
          </div>
          <div class="setting-item">
            <label>字幕翻译模型</label>
            <input type="text" v-model.trim="settingsForm.subtitle_translation_model" class="text-input" placeholder="gpt-4o-mini 或本地兼容模型" />
          </div>
        </template>
      </template>
    </div>

    <div class="settings-section">
      <h3>字幕识别质量</h3>
      <div class="setting-item">
        <label>WhisperX 模型</label>
        <select v-model="settingsForm.subtitle_whisperx_model" class="select-input">
          <option value="tiny">tiny（最快）</option>
          <option value="base">base</option>
          <option value="small">small</option>
          <option value="medium">medium（推荐）</option>
          <option value="large-v2">large-v2</option>
          <option value="large-v3">large-v3（最准确）</option>
        </select>
      </div>
      <div class="setting-item">
        <label>WhisperX 批量大小</label>
        <input type="number" min="1" max="16" v-model.number="settingsForm.subtitle_whisperx_batch_size" class="number-input" />
        <p class="help-text">CPU 运行时建议使用 4-8；数值越大占用内存越多。</p>
      </div>
    </div>

    <!-- 扫描目录管理 -->
    <div class="settings-section">
      <h3>扫描目录管理</h3>
      <div class="directories-list">
        <div v-for="dir in localDirectories" :key="dir.id" class="directory-item">
          <div class="directory-main">
            <strong>{{ dir.alias || '未命名' }}</strong>
            <span>{{ dir.path }}</span>
            <div class="directory-watch-status" :class="`directory-watch-status--${directoryWatchState(dir.id)}`">
              <span>{{ directoryWatchText(dir.id) }}</span>
              <button
                v-if="settingsForm.library_watch_enabled && ['unavailable', 'error'].includes(directoryWatchState(dir.id))"
                type="button"
                class="btn-link"
                :data-test="`retry-library-watch-${dir.id}`"
                @click="retryDirectoryWatch(dir.id)"
              >重试</button>
            </div>
          </div>
          <div class="directory-actions">
            <button @click="editDirectory(dir)" class="btn-secondary">编辑</button>
            <button @click="deleteDirectoryItem(dir.id)" class="btn-secondary btn-danger-outline">删除</button>
          </div>
        </div>
        <div v-if="localDirectories.length === 0" class="empty-hint">暂无扫描目录配置</div>
      </div>
      <button @click="showAddDirectoryDialog = true" class="btn-primary settings-section-action">添加扫描目录</button>
    </div>

    <div class="settings-section backup-settings-section">
      <h3>数据库备份</h3>
      <div class="backup-status" :class="{ 'backup-status--error': backupStatus && !backupStatus.available }">
        <strong>{{ backupStatusText }}</strong>
        <span v-if="backupStatus?.last_success_at">最近成功：{{ formatBackupTime(backupStatus.last_success_at) }}</span>
        <span v-if="backupStatus?.last_error">最近失败：{{ backupStatus.last_error }}</span>
        <span v-else-if="backupStatus?.reason">{{ backupStatus.reason }}</span>
      </div>
      <div class="setting-item">
        <label>备份目录</label>
        <div class="directory-dialog-row">
          <input type="text" v-model.trim="settingsForm.backup_directory" class="text-input" placeholder="留空使用应用数据目录" />
          <button type="button" class="btn-secondary" @click="selectBackupDirectory">选择</button>
        </div>
        <p class="help-text">留空时保存到应用数据目录下的 backups 文件夹。</p>
      </div>
      <div class="setting-grid backup-setting-grid">
        <div class="setting-item">
          <label>保留份数</label>
          <input type="number" min="1" max="100" v-model.number="settingsForm.backup_retention_count" class="number-input" />
        </div>
        <div class="setting-item">
          <label>自动备份间隔（小时）</label>
          <input type="number" min="0" max="8760" v-model.number="settingsForm.backup_interval_hours" class="number-input" />
          <p class="help-text">0 表示关闭启动时自动备份。</p>
        </div>
      </div>
      <div class="backup-actions">
        <button type="button" class="btn-primary" :disabled="backupBusy || !backupStatus?.backup_available" @click="createBackupNow">
          {{ backupBusy ? '处理中...' : '立即备份' }}
        </button>
        <button type="button" class="btn-secondary" :disabled="backupBusy || !backupStatus?.restore_available" @click="openBackupDialog">从备份恢复</button>
        <button type="button" class="btn-secondary" :disabled="backupBusy" @click="loadBackupStatus">刷新状态</button>
      </div>
      <p v-if="backupMessage" class="help-text" :class="{ 'backup-message--error': backupMessageIsError }">{{ backupMessage }}</p>
    </div>
    </div>

    <div class="settings-actions">
      <div
        v-if="saveMessage"
        class="settings-save-status"
        :class="`settings-save-status--${saveState}`"
        :role="saveState === 'error' ? 'alert' : 'status'"
      >
        {{ saveMessage }}
      </div>
      <button @click="saveSettings" class="btn-primary settings-save-button" :disabled="settingsSaving">
        {{ settingsSaving ? '正在保存...' : '保存所有设置' }}
      </button>
    </div>

    <!-- Add/Edit Directory Dialog -->
    <BaseModal v-if="showAddDirectoryDialog || editingDirectory" close-on-overlay stop-modal-clicks @close="closeDirectoryDialog">
        <h2>{{ editingDirectory ? '编辑' : '添加' }}扫描目录</h2>
        <div class="setting-item">
          <label>目录路径</label>
          <div class="directory-dialog-row">
            <input type="text" v-model="directoryForm.path" placeholder="选择目录" class="text-input" readonly />
            <button @click="selectDirectoryForConfig" class="btn-secondary">选择</button>
          </div>
        </div>
        <div class="setting-item">
          <label>目录别名</label>
          <input type="text" v-model="directoryForm.alias" placeholder="给这个目录起个名字" class="text-input directory-alias-input" />
        </div>
        <div class="modal-actions">
          <button @click="saveDirectoryConfig" class="btn-primary">保存</button>
          <button @click="closeDirectoryDialog" class="btn-secondary">取消</button>
        </div>
    </BaseModal>

    <BaseModal v-if="showBackupDialog" stop-modal-clicks @close="closeBackupDialog">
      <template v-if="!selectedBackup">
        <h2>选择数据库备份</h2>
        <p class="help-text">恢复会替换当前数据库。执行前系统会先自动创建一份当前数据库的安全备份。</p>
        <div v-if="backupFiles.length" class="backup-file-list">
          <div v-for="backup in backupFiles" :key="backup.name" class="backup-file-item">
            <div>
              <strong>{{ formatBackupTime(backup.created_at) }}</strong>
              <span>{{ backup.name }} · {{ formatBackupSize(backup.size) }}</span>
            </div>
            <button type="button" class="btn-secondary btn-danger-outline" @click="selectedBackup = backup">恢复</button>
          </div>
        </div>
        <p v-else class="empty-hint">当前目录没有可用备份。</p>
        <div class="modal-actions"><button type="button" class="btn-secondary" @click="closeBackupDialog">关闭</button></div>
      </template>
      <template v-else>
        <h2>确认恢复数据库</h2>
        <p>将恢复到 <strong>{{ formatBackupTime(selectedBackup.created_at) }}</strong> 的数据状态。</p>
        <p class="backup-danger-text">这是破坏性操作。当前数据库会先自动备份；恢复期间请勿关闭应用。</p>
        <div class="modal-actions">
          <button type="button" class="btn-secondary" :disabled="backupBusy" @click="selectedBackup = null">返回</button>
          <button type="button" class="btn-primary btn-danger" :disabled="backupBusy" @click="confirmRestoreBackup">
            {{ backupBusy ? '正在恢复...' : '确认恢复' }}
          </button>
        </div>
      </template>
    </BaseModal>

	<BaseModal v-if="showShortcutHelp" stop-modal-clicks @close="showShortcutHelp = false">
	  <h2>键盘快捷键</h2>
	  <dl class="shortcut-help-list">
		<dt>J / ↓</dt><dd>选择下一个视频</dd>
		<dt>K / ↑</dt><dd>选择上一个视频</dd>
		<dt>空格</dt><dd>打开或关闭详情预览</dd>
		<dt>F</dt><dd>切换收藏</dd>
		<dt>W</dt><dd>切换已看</dd>
		<dt>T</dt><dd>打开添加标签</dd>
		<dt>回车</dt><dd>播放视频</dd>
	  </dl>
	  <p class="help-text">输入框、下拉框和弹窗处于焦点时不会触发快捷键。</p>
	  <div class="modal-actions"><button type="button" class="btn-primary" @click="showShortcutHelp = false">知道了</button></div>
	</BaseModal>
  </div>
</template>

<script>
import { UpdateSettings, SelectDirectory, GetAllDirectories, AddDirectory, UpdateDirectory, DeleteDirectory, GetShortFeedServerStatus, GetAITagLibrary, SaveAITagLibrary, TriggerAITagging, GetLibraryWatcherStatus, RetryLibraryWatcherRoot, GetBackupStatus, ListDatabaseBackups, CreateDatabaseBackup, RestoreDatabaseBackup, GetSemanticIndexStatus, StartSemanticIndex, CancelSemanticIndex } from '../../wailsjs/go/main/App';
import { flattenAITagGroups, groupAITagsByNamespace, validateAITagGroups } from '../utils/aiTagLibrary.js';
import BaseModal from './ui/BaseModal.vue';

export default {
  name: 'SettingsPage',
  components: { BaseModal },
  props: {
    settings: { type: Object, required: true },
    directories: { type: Array, default: () => [] }
  },
  emits: ['settings-saved', 'directories-changed', 'tags-changed'],
  data() {
    return {
      settingsForm: { ...this.settings },
      localDirectories: [...this.directories],
      shortFeedStatus: null,
      watcherStatus: null,
      watcherStatusOff: null,
      semanticIndexStatusOff: null,
      semanticIndexStatus: null,
      showAddDirectoryDialog: false,
      editingDirectory: null,
      directoryForm: { path: '', alias: '' },
      localAITagGroups: [],
      aiTagLibraryLoading: false,
      aiTagLibraryError: '',
      nextAITagLibraryKey: 1,
      settingsSaving: false,
      saveState: 'idle',
      saveMessage: '',
      backupStatus: null,
      backupFiles: [],
      showBackupDialog: false,
	  showShortcutHelp: false,
      selectedBackup: null,
      backupBusy: false,
      backupMessage: '',
      backupMessageIsError: false
    };
  },
  watch: {
    settings: {
      handler(val) {
        this.settingsForm = { ...val };
        if (!this.settingsForm.video_extensions || this.settingsForm.video_extensions.trim() === '') {
          this.settingsForm.video_extensions = '.mp4,.avi,.mkv,.mov,.wmv,.flv,.webm,.m4v,.ts,.3gp,.mpg,.mpeg,.rm,.rmvb,.vob,.divx,.f4v,.asf,.qt';
        }
        this.settingsForm.ai_tagging_images_per_request = this.settingsForm.ai_tagging_images_per_request || 10;
        this.settingsForm.ai_tagging_subtitle_char_limit = this.settingsForm.ai_tagging_subtitle_char_limit || 4000;
        this.settingsForm.ai_tagging_startup_batch_size = this.settingsForm.ai_tagging_startup_batch_size || 10;
        this.settingsForm.ai_tagging_max_extra_frames = this.settingsForm.ai_tagging_max_extra_frames || 20;
        this.settingsForm.semantic_embedding_model = this.settingsForm.semantic_embedding_model || '';
        this.settingsForm.short_feed_max_duration_minutes = this.settingsForm.short_feed_max_duration_minutes || 5;
		if (this.settingsForm.short_feed_feedback_sync_enabled == null) this.settingsForm.short_feed_feedback_sync_enabled = true;
        this.settingsForm.scan_exclude_paths = this.settingsForm.scan_exclude_paths || '';
        this.settingsForm.subtitle_translation_provider = this.settingsForm.subtitle_translation_provider || 'deepl';
        this.settingsForm.subtitle_whisperx_model = this.settingsForm.subtitle_whisperx_model || 'medium';
        this.settingsForm.subtitle_whisperx_batch_size = this.settingsForm.subtitle_whisperx_batch_size || 8;
        this.settingsForm.backup_directory = this.settingsForm.backup_directory || '';
        this.settingsForm.backup_retention_count = this.settingsForm.backup_retention_count || 7;
        this.settingsForm.backup_interval_hours = Number.isFinite(this.settingsForm.backup_interval_hours) ? this.settingsForm.backup_interval_hours : 24;
        this.settingsForm.random_half_life_days = Number.isFinite(this.settingsForm.random_half_life_days) ? this.settingsForm.random_half_life_days : 90;
      },
      immediate: true,
      deep: true
    },
    directories: {
      handler(val) {
        this.localDirectories = [...val];
      },
      immediate: true,
      deep: true
    }
  },
  computed: {
    scanExcludePaths() {
      return [...new Set(String(this.settingsForm.scan_exclude_paths || '').split(/\r?\n/).map(path => path.trim()).filter(Boolean))];
    },
    shortFeedStatusText() {
      if (!this.shortFeedStatus) return '未加载';
      if (this.shortFeedStatus.running) {
        return this.shortFeedStatus.fallback_used ? '运行中（备用端口）' : '运行中';
      }
      return '未运行';
    },
    backupStatusText() {
      if (!this.backupStatus) return '正在读取备份状态...';
      if (this.backupStatus.running) return '备份任务运行中';
      return this.backupStatus.available ? '备份功能可用' : '备份功能不可用';
    },
    semanticIndexStatusText() {
      const status = this.semanticIndexStatus;
      if (!status) return '正在读取语义索引状态...';
      if (!status.available) return '语义索引不可用';
      if (status.running) return '语义索引构建中';
      if (status.needs_rebuild) return '模型或维度已变化，需要重建';
      if (status.cancelled) return '语义索引构建已取消，可继续';
      if (status.completed) return '语义索引构建完成';
      return '语义索引可用，尚未构建';
    }
  },
  mounted() {
    this.loadShortFeedStatus();
    this.loadAITagLibrary();
    this.loadLibraryWatcherStatus();
    this.loadBackupStatus();
    this.loadSemanticIndexStatus();
    if (window.runtime?.EventsOn) {
      const off = window.runtime.EventsOn('library-watcher-status', (status) => {
        this.watcherStatus = status || null;
      });
      if (typeof off === 'function') this.watcherStatusOff = off;
      const semanticOff = window.runtime.EventsOn('semantic-index-state', (status) => {
        this.semanticIndexStatus = { ...(this.semanticIndexStatus || {}), ...(status || {}) };
      });
      if (typeof semanticOff === 'function') this.semanticIndexStatusOff = semanticOff;
    }
  },
  beforeUnmount() {
    this.watcherStatusOff?.();
    this.semanticIndexStatusOff?.();
  },
  methods: {
    async loadSemanticIndexStatus() {
      try {
        this.semanticIndexStatus = await GetSemanticIndexStatus();
      } catch (err) {
        this.semanticIndexStatus = { available: false, unavailable: String(err) };
      }
    },
    async startSemanticIndex(rebuild) {
      if (rebuild && !window.confirm('重建会为当前模型重新请求全部视频的 embedding。继续吗？')) return;
      try {
        this.semanticIndexStatus = { ...(this.semanticIndexStatus || {}), ...(await StartSemanticIndex({ rebuild })) };
      } catch (err) {
        alert((rebuild ? '重建' : '启动') + '语义索引失败：' + err);
        await this.loadSemanticIndexStatus();
      }
    },
    async cancelSemanticIndex() {
      try {
        await CancelSemanticIndex();
        await this.loadSemanticIndexStatus();
      } catch (err) {
        alert('取消语义索引失败：' + err);
      }
    },
    async loadShortFeedStatus() {
      try {
        this.shortFeedStatus = await GetShortFeedServerStatus();
      } catch (err) {
        this.shortFeedStatus = {
          running: false,
          startup_error: String(err)
        };
      }
    },
    openShortFeed() {
      if (this.shortFeedStatus?.url) {
        window.open(this.shortFeedStatus.url, '_blank', 'noopener,noreferrer');
      }
    },
    async saveSettings() {
      if (this.settingsSaving) return;
      this.settingsSaving = true;
      this.saveState = 'saving';
      this.saveMessage = '正在保存设置...';
      try {
        this.aiTagLibraryError = '';
        const validationError = validateAITagGroups(this.localAITagGroups);
        if (validationError) throw new Error(validationError);
        const tagInputs = flattenAITagGroups(this.localAITagGroups);
        const [savedTags] = await Promise.all([
          SaveAITagLibrary(tagInputs),
          UpdateSettings({
            confirm_before_delete: this.settingsForm.confirm_before_delete,
            delete_original_file: this.settingsForm.delete_original_file,
            video_extensions: this.settingsForm.video_extensions,
            scan_exclude_paths: this.settingsForm.scan_exclude_paths || '',
            play_weight: this.settingsForm.play_weight,
            random_half_life_days: Math.max(0, Number(this.settingsForm.random_half_life_days) || 0),
            auto_scan_on_startup: this.settingsForm.auto_scan_on_startup,
            library_watch_enabled: this.settingsForm.library_watch_enabled || false,
            local_metadata_enabled: this.settingsForm.local_metadata_enabled || false,
            ai_quality_enabled: this.settingsForm.ai_quality_enabled || false,
            short_feed_max_duration_minutes: this.settingsForm.short_feed_max_duration_minutes || 5,
			short_feed_feedback_sync_enabled: this.settingsForm.short_feed_feedback_sync_enabled !== false,
            theme: this.settingsForm.theme,
            log_enabled: this.settingsForm.log_enabled,
            bilingual_enabled: this.settingsForm.bilingual_enabled || false,
            bilingual_lang: this.settingsForm.bilingual_lang || 'zh',
            deepl_api_key: this.settingsForm.deepl_api_key || '',
            subtitle_translation_provider: this.settingsForm.subtitle_translation_provider || 'deepl',
            subtitle_translation_base_url: this.settingsForm.subtitle_translation_base_url || '',
            subtitle_translation_api_key: this.settingsForm.subtitle_translation_api_key || '',
            subtitle_translation_model: this.settingsForm.subtitle_translation_model || '',
            subtitle_whisperx_model: this.settingsForm.subtitle_whisperx_model || 'medium',
            subtitle_whisperx_batch_size: this.settingsForm.subtitle_whisperx_batch_size || 8,
            ai_tagging_base_url: this.settingsForm.ai_tagging_base_url || '',
            ai_tagging_api_key: this.settingsForm.ai_tagging_api_key || '',
            ai_tagging_model: this.settingsForm.ai_tagging_model || '',
            semantic_embedding_model: this.settingsForm.semantic_embedding_model || '',
            ai_tagging_frame_count: 0,
            ai_tagging_images_per_request: this.settingsForm.ai_tagging_images_per_request || 10,
            ai_tagging_subtitle_char_limit: this.settingsForm.ai_tagging_subtitle_char_limit || 4000,
            ai_tagging_startup_batch_size: this.settingsForm.ai_tagging_startup_batch_size || 10,
            ai_tagging_max_extra_frames: this.settingsForm.ai_tagging_max_extra_frames || 20,
            backup_directory: this.settingsForm.backup_directory || '',
            backup_retention_count: this.settingsForm.backup_retention_count || 7,
            backup_interval_hours: Math.max(0, Number(this.settingsForm.backup_interval_hours) || 0)
          })
        ]);
        const aiTriggered = await TriggerAITagging();
        await this.loadLibraryWatcherStatus();
        this.localAITagGroups = this.withAITagLibraryKeys(savedTags);
        this.$emit('settings-saved', { ...this.settingsForm });
        this.$emit('tags-changed');
        const hasActiveTags = tagInputs.some(tag => tag.is_active);
        const hasAIConfig = Boolean(String(this.settingsForm.ai_tagging_base_url || '').trim() && String(this.settingsForm.ai_tagging_model || '').trim());
        this.saveState = 'success';
        this.saveMessage = hasActiveTags && hasAIConfig && aiTriggered
          ? '设置保存成功，已触发 AI 自动打标。'
          : '设置保存成功；AI 接口、模型、启用标签或后台任务未就绪，自动打标暂未启动。';
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err);
        this.saveState = 'error';
        this.saveMessage = '设置保存失败：' + message;
        console.error('保存设置失败:', err);
      } finally {
        this.settingsSaving = false;
      }
    },
    async loadAITagLibrary() {
      this.aiTagLibraryLoading = true;
      this.aiTagLibraryError = '';
      try {
        this.localAITagGroups = this.withAITagLibraryKeys(await GetAITagLibrary());
      } catch (err) {
        this.aiTagLibraryError = '加载 AI 标签库失败: ' + err;
      } finally {
        this.aiTagLibraryLoading = false;
      }
    },
    async loadLibraryWatcherStatus() {
      try {
        this.watcherStatus = await GetLibraryWatcherStatus();
      } catch (_err) {
        this.watcherStatus = null;
      }
    },
    async loadBackupStatus() {
      try {
        this.backupStatus = await GetBackupStatus();
      } catch (err) {
        this.backupStatus = { available: false, backup_available: false, restore_available: false, reason: String(err) };
      }
    },
    async selectBackupDirectory() {
      try {
        const directory = await SelectDirectory();
        if (directory) this.settingsForm.backup_directory = directory;
      } catch (err) {
        this.backupMessageIsError = true;
        this.backupMessage = '选择备份目录失败：' + err;
      }
    },
    async createBackupNow() {
      if (this.backupBusy) return;
      this.backupBusy = true;
      this.backupMessage = '正在创建并校验数据库备份...';
      this.backupMessageIsError = false;
      try {
        const backup = await CreateDatabaseBackup();
        this.backupMessage = `备份成功：${backup.name}`;
      } catch (err) {
        this.backupMessageIsError = true;
        this.backupMessage = '备份失败：' + err;
      } finally {
        this.backupBusy = false;
        await this.loadBackupStatus();
      }
    },
    async openBackupDialog() {
      this.backupMessage = '';
      this.backupMessageIsError = false;
      try {
        this.backupFiles = await ListDatabaseBackups();
        this.selectedBackup = null;
        this.showBackupDialog = true;
      } catch (err) {
        this.backupMessageIsError = true;
        this.backupMessage = '读取备份列表失败：' + err;
      }
    },
    closeBackupDialog() {
      if (this.backupBusy) return;
      this.showBackupDialog = false;
      this.selectedBackup = null;
    },
    async confirmRestoreBackup() {
      if (!this.selectedBackup || this.backupBusy) return;
      this.backupBusy = true;
      try {
        await RestoreDatabaseBackup({
          name: this.selectedBackup.name,
          size: this.selectedBackup.size,
          fingerprint: this.selectedBackup.fingerprint
        });
        this.backupMessage = '数据库恢复成功，应用将自动退出；重新打开后即可使用恢复的数据。';
        this.backupMessageIsError = false;
        this.showBackupDialog = false;
        this.selectedBackup = null;
      } catch (err) {
        this.backupMessageIsError = true;
        this.backupMessage = '数据库恢复失败：' + err;
      } finally {
        this.backupBusy = false;
        await this.loadBackupStatus();
      }
    },
    formatBackupTime(value) {
      if (!value) return '未知时间';
      const date = new Date(value);
      return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleString();
    },
    formatBackupSize(value) {
      const bytes = Number(value) || 0;
      if (bytes < 1024) return `${bytes} B`;
      if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
      return `${(bytes / 1024 / 1024).toFixed(1)} MiB`;
    },
    directoryWatchStatus(directoryID) {
      return this.watcherStatus?.roots?.find(root => Number(root.directory_id) === Number(directoryID)) || null;
    },
    directoryWatchState(directoryID) {
      if (!this.settingsForm.library_watch_enabled) return 'disabled';
      return this.directoryWatchStatus(directoryID)?.state || 'error';
    },
    directoryWatchText(directoryID) {
      if (!this.settingsForm.library_watch_enabled) return '实时同步已关闭';
      const status = this.directoryWatchStatus(directoryID);
      if (!status) return '实时同步状态未知';
      if (status.state === 'watching') return `实时同步中（${status.watch_count || 0} 个目录）`;
      return status.message || (status.state === 'unavailable' ? '当前不可用' : '监听错误');
    },
    async retryDirectoryWatch(directoryID) {
      try {
        await RetryLibraryWatcherRoot(directoryID);
      } finally {
        await this.loadLibraryWatcherStatus();
      }
    },
    withAITagLibraryKeys(tags) {
      return groupAITagsByNamespace(tags).map(group => ({
        ...group,
        _key: `ai-tag-group-${this.nextAITagLibraryKey++}`,
        tags: group.tags.map(tag => ({ ...tag, _key: `ai-tag-${tag.id || 'new'}-${this.nextAITagLibraryKey++}` }))
      }));
    },
    newAITagLibraryItem() {
      return {
        id: 0,
        name: '',
        color: '#0D9488',
        review_required: false,
        is_active: true,
        _key: `ai-tag-new-${this.nextAITagLibraryKey++}`
      };
    },
    addAITagLibraryGroup() {
      this.localAITagGroups = [...this.localAITagGroups, {
        namespace: '',
        tags: [this.newAITagLibraryItem()],
        _key: `ai-tag-group-new-${this.nextAITagLibraryKey++}`
      }];
    },
    removeAITagLibraryGroup(groupIndex) {
      this.localAITagGroups = this.localAITagGroups.filter((_, index) => index !== groupIndex);
    },
    addAITagToGroup(groupIndex) {
      const group = this.localAITagGroups[groupIndex];
      if (!group) return;
      group.tags = [...group.tags, this.newAITagLibraryItem()];
    },
    removeAITagFromGroup(groupIndex, tagIndex) {
      const group = this.localAITagGroups[groupIndex];
      if (!group) return;
      group.tags = group.tags.filter((_, index) => index !== tagIndex);
    },
    async addScanExcludeDirectory() {
      try {
        const path = await SelectDirectory();
        if (!path) return;
        this.settingsForm.scan_exclude_paths = [...new Set([...this.scanExcludePaths, path])].join('\n');
      } catch (err) {
        this.saveState = 'error';
        this.saveMessage = '选择黑名单目录失败：' + err;
      }
    },
    removeScanExcludeDirectory(path) {
      this.settingsForm.scan_exclude_paths = this.scanExcludePaths.filter(item => item !== path).join('\n');
    },
    async selectDirectoryForConfig() {
      try {
        const dir = await SelectDirectory();
        if (dir) this.directoryForm.path = dir;
      } catch (err) {}
    },
    editDirectory(dir) {
      this.editingDirectory = dir;
      this.directoryForm = { path: dir.path, alias: dir.alias };
    },
    async saveDirectoryConfig() {
      if (!this.directoryForm.path) return;
      try {
        if (this.editingDirectory) {
          await UpdateDirectory(this.editingDirectory.id, this.directoryForm.path, this.directoryForm.alias);
        } else {
          await AddDirectory(this.directoryForm.path, this.directoryForm.alias);
        }
        await this.refreshDirectories();
        this.closeDirectoryDialog();
      } catch (err) {}
    },
    async deleteDirectoryItem(id) {
      if (!confirm('确定要删除此目录配置吗？')) return;
      try {
        await DeleteDirectory(id);
        await this.refreshDirectories();
      } catch (err) {}
    },
    async refreshDirectories() {
      try {
        this.localDirectories = await GetAllDirectories();
        await this.loadLibraryWatcherStatus();
        this.$emit('directories-changed', this.localDirectories);
      } catch (err) {}
    },
    closeDirectoryDialog() {
      this.showAddDirectoryDialog = false;
      this.editingDirectory = null;
      this.directoryForm = { path: '', alias: '' };
    }
  }
};
</script>

<style scoped>
.settings-page h2 {
  margin: 0 0 14px;
  font-size: 22px;
  font-weight: 760;
}

.settings-grid-shell {
  display: grid;
  grid-template-columns: repeat(2, minmax(320px, 1fr));
  gap: 14px;
  align-items: start;
}

.settings-section {
  margin-bottom: 0;
  padding: 18px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius-lg);
  background: var(--panel-bg);
  -webkit-backdrop-filter: blur(14px);
  backdrop-filter: blur(14px);
}

.settings-section h3 {
  font-size: 16px;
  font-weight: 600;
  color: var(--text-primary);
  border-bottom: 1px solid var(--border-color);
  margin-bottom: 16px;
  padding-bottom: 10px;
}

.setting-grid { display: grid; grid-template-columns: repeat(3, minmax(160px, 1fr)); gap: 16px; }
.setting-grid .setting-item { margin-bottom: 0; }

@media (max-width: 760px) {
  .setting-grid { grid-template-columns: 1fr; }
}

.settings-section-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.settings-section-heading h3 {
  margin-bottom: 0;
}

.ai-tag-library-section {
  grid-column: 1 / -1;
}

.ai-tag-library-list {
  display: grid;
  gap: 0;
  max-height: 420px;
  margin-top: 12px;
  padding-right: 4px;
  overflow-y: auto;
}

.ai-tag-library-group {
  display: grid;
  grid-template-columns: minmax(220px, 0.7fr) minmax(0, 2fr);
  gap: 14px;
  padding: 12px 0;
  border-bottom: 1px solid var(--border-color);
}

.ai-tag-library-group:first-child {
  border-top: 1px solid var(--border-color);
}

.ai-tag-library-group-heading {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-content: start;
  align-items: center;
  gap: 8px;
}

.ai-tag-namespace-input {
  font-weight: 650;
}

.ai-tag-count {
  color: var(--text-secondary);
  font-size: 12px;
  white-space: nowrap;
}

.ai-tag-library-group-heading .btn-compact {
  width: 100%;
}

.ai-tag-library-group-tags {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 8px;
}

.ai-tag-library-tag {
  display: grid;
  grid-template-columns: minmax(120px, 1fr) 32px auto 32px;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.ai-tag-group-empty {
  align-self: center;
  text-align: left;
}

.ai-tag-color {
  width: 32px;
  height: 32px;
  padding: 0;
  border: 1px solid var(--border-color);
  border-radius: 4px;
  background: transparent;
}

.ai-tag-active {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  white-space: nowrap;
}

.ai-tag-remove {
  width: 32px;
  height: 32px;
  padding: 0;
}

.ai-tag-library-error {
  margin-top: 10px;
  color: var(--danger-color);
  font-size: 13px;
}

.setting-control-row {
  margin-top: 10px;
}
.scan-blacklist-setting { margin-top: 18px; padding-top: 16px; border-top: 1px solid var(--border-color); }.scan-blacklist-list { display: grid; gap: 7px; margin-top: 10px; }.scan-blacklist-item { display: flex; align-items: center; gap: 8px; padding: 7px 8px; border: 1px solid var(--border-color); border-radius: 8px; background: var(--control-bg); }.scan-blacklist-item span { min-width: 0; flex: 1; overflow: hidden; color: var(--text-secondary); font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }

.settings-textarea {
  height: auto;
  min-height: 80px;
  padding: 12px;
  resize: vertical;
}

.directories-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.directory-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 11px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius-md);
  background: var(--surface-faint);
}

.directory-main {
  flex: 1;
  min-width: 0;
}

.directory-main strong,
.directory-main span {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.directory-main strong {
  margin-bottom: 4px;
  font-size: 14px;
}

.directory-main span {
  color: var(--text-secondary);
  font-size: 12px;
}

.directory-watch-status {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 7px;
}

.directory-watch-status::before {
  width: 7px;
  height: 7px;
  flex: 0 0 7px;
  border-radius: 50%;
  background: var(--text-secondary);
  content: '';
}

.directory-watch-status--watching::before {
  background: #22a06b;
}

.directory-watch-status--error::before,
.directory-watch-status--unavailable::before {
  background: var(--danger-color);
}

.directory-watch-status .btn-link {
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--accent-color);
  cursor: pointer;
  font-size: 12px;
}

.directory-actions,
.directory-dialog-row {
  display: flex;
  gap: 8px;
}

.directory-dialog-row .text-input {
  flex: 1;
}

.directory-alias-input {
  margin-top: 8px;
}

.settings-section-action {
  margin-top: 15px;
}

.settings-actions {
  position: sticky;
  bottom: 0;
  z-index: 20;
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 12px;
  margin-top: 16px;
  padding: 12px 0 6px;
  background: linear-gradient(180deg, transparent, var(--bg-color) 32%);
}

.settings-save-button {
  flex-shrink: 0;
  padding: 0 32px;
}

.settings-save-status {
  min-width: 0;
  padding: 8px 10px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--control-bg);
  color: var(--text-secondary);
  font-size: 13px;
  text-align: left;
}

.settings-save-status--success {
  border-color: var(--accent-border);
  color: var(--accent-color);
}

.settings-save-status--error {
  border-color: var(--danger-border);
  color: var(--danger-color);
}

.short-feed-status {
  display: flex;
  gap: 12px;
  align-items: center;
  justify-content: space-between;
  padding: 14px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--bg-color);
}

.short-feed-status-main {
  display: grid;
  gap: 6px;
  min-width: 0;
}

.short-feed-status-main span,
.short-feed-url {
  overflow: hidden;
  color: var(--text-secondary);
  font-size: 13px;
  white-space: nowrap;
  text-overflow: ellipsis;
}

.short-feed-actions {
  display: flex;
  flex-shrink: 0;
  gap: 8px;
}

.short-feed-lan-list {
  display: grid;
  gap: 6px;
  margin-top: 10px;
}

.short-feed-url {
  padding: 8px 10px;
  border-radius: 6px;
  background: var(--bg-color);
}

.backup-settings-section {
  grid-column: 1 / -1;
}

.backup-status {
  display: grid;
  gap: 5px;
  margin-bottom: 16px;
  padding: 12px;
  border: 1px solid var(--accent-border);
  border-radius: var(--radius);
  background: var(--accent-soft);
}

.backup-status--error {
  border-color: var(--danger-border);
  background: var(--danger-soft);
}

.backup-status span,
.backup-file-item span {
  color: var(--text-secondary);
  font-size: 12px;
}

.shortcut-help-list {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 10px 18px;
  margin: 18px 0;
}

.shortcut-help-list dt {
  color: var(--text-primary);
  font-weight: 700;
}

.shortcut-help-list dd {
  margin: 0;
  color: var(--text-secondary);
}

.backup-setting-grid {
  grid-template-columns: repeat(2, minmax(180px, 1fr));
}

.backup-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 14px;
}

.backup-message--error,
.backup-danger-text {
  color: var(--danger-color);
}

.backup-file-list {
  display: grid;
  gap: 8px;
  max-height: 360px;
  margin-top: 16px;
  overflow-y: auto;
}

.backup-file-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 10px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--control-bg);
}

.backup-file-item div {
  display: grid;
  gap: 4px;
  min-width: 0;
}

.backup-file-item span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

@media (max-width: 980px) {
  .settings-grid-shell {
    grid-template-columns: 1fr;
  }

  .directory-item {
    align-items: stretch;
    flex-direction: column;
  }

  .directory-actions {
    justify-content: flex-end;
  }

  .ai-tag-library-group {
    grid-template-columns: minmax(190px, 0.65fr) minmax(0, 1.8fr);
  }
}

@media (max-width: 600px) {
  .ai-tag-library-group {
    grid-template-columns: 1fr;
  }

  .ai-tag-library-group-tags {
    grid-template-columns: 1fr;
  }

  .settings-actions {
    align-items: stretch;
    flex-direction: column;
  }

  .settings-save-button {
    width: 100%;
  }
}
</style>
