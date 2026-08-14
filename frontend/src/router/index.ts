import { createRouter } from 'vue-router'
import { createQMediaSyncHashHistory } from './history'
import { createAsyncRouteComponent } from './asyncRoute'
import { useAuthStore } from '@/stores/auth'
import { http } from '@/http/client'

const AppHome = createAsyncRouteComponent('AppHome', () => import('@/components/AppHome.vue'))
const AppLogPage = createAsyncRouteComponent('AppLogPage', () => import('@/components/AppLogPage.vue'))
const AppLogin = createAsyncRouteComponent('AppLogin', () => import('@/components/AppLogin.vue'))
const AppUserSettings = createAsyncRouteComponent(
  'AppUserSettings',
  () => import('@/components/AppUserSettings.vue'),
)
const AppStrmSettings = createAsyncRouteComponent(
  'AppStrmSettings',
  () => import('@/components/AppStrmSettings.vue'),
)
const AppEmbySettings = createAsyncRouteComponent(
  'AppEmbySettings',
  () => import('@/components/AppEmbySettings.vue'),
)
const AppSyncRecords = createAsyncRouteComponent(
  'AppSyncRecords',
  () => import('@/components/AppSyncRecords.vue'),
)
const AppSyncTaskDetail = createAsyncRouteComponent(
  'AppSyncTaskDetail',
  () => import('@/components/AppSyncTaskDetail.vue'),
)
const AppSyncDirectories = createAsyncRouteComponent(
  'AppSyncDirectories',
  () => import('@/components/AppSyncDirectories.vue'),
)
const AppSyncDirectoryForm = createAsyncRouteComponent(
  'AppSyncDirectoryForm',
  () => import('@/components/AppSyncDirectoryForm.vue'),
)
const AppCloudAccounts = createAsyncRouteComponent(
  'AppCloudAccounts',
  () => import('@/components/AppCloudAccounts.vue'),
)
const AppThreadSettings = createAsyncRouteComponent(
  'AppThreadSettings',
  () => import('@/components/AppThreadSettings.vue'),
)
const AppLogSettings = createAsyncRouteComponent(
  'AppLogSettings',
  () => import('@/components/AppLogSettings.vue'),
)
const AppTmdbSettings = createAsyncRouteComponent(
  'AppTmdbSettings',
  () => import('@/components/AppTmdbSettings.vue'),
)
const AppAiSettings = createAsyncRouteComponent(
  'AppAiSettings',
  () => import('@/components/AppAiSettings.vue'),
)
const AppCategoryStrategy = createAsyncRouteComponent(
  'AppCategoryStrategy',
  () => import('@/components/AppCategoryStrategy.vue'),
)
const AppScrapePathes = createAsyncRouteComponent(
  'AppScrapePathes',
  () => import('@/components/AppScrapePathes.vue'),
)
const AppScrapePathForm = createAsyncRouteComponent(
  'AppScrapePathForm',
  () => import('@/components/AppScrapePathForm.vue'),
)
const AppScrapeRecords = createAsyncRouteComponent(
  'AppScrapeRecords',
  () => import('@/components/AppScrapeRecords.vue'),
)
const AppUploadQueue = createAsyncRouteComponent(
  'AppUploadQueue',
  () => import('@/components/AppUploadQueue.vue'),
)
const AppDownloadQueue = createAsyncRouteComponent(
  'AppDownloadQueue',
  () => import('@/components/AppDownloadQueue.vue'),
)
const AppNotificationChannels = createAsyncRouteComponent(
  'AppNotificationChannels',
  () => import('@/components/AppNotificationChannels.vue'),
)
const AppMoviePilotSettings = createAsyncRouteComponent(
  'AppMoviePilotSettings',
  () => import('@/components/AppMoviePilotSettings.vue'),
)
const AppMoviePilotSubscribes = createAsyncRouteComponent(
  'AppMoviePilotSubscribes',
  () => import('@/components/AppMoviePilotSubscribes.vue'),
)
const AppMoviePilotFailed = createAsyncRouteComponent(
  'AppMoviePilotFailed',
  () => import('@/components/AppMoviePilotFailed.vue'),
)
const AppApiKeys = createAsyncRouteComponent(
  'AppApiKeys',
  () => import('@/components/AppApiKeys.vue'),
)
const AppLoginSessions = createAsyncRouteComponent(
  'AppLoginSessions',
  () => import('@/components/user-settings/LoginSessions.vue'),
)
const AppFileManager = createAsyncRouteComponent(
  'AppFileManager',
  () => import('@/components/AppFileManager.vue'),
)
const AppUpdate = createAsyncRouteComponent('AppUpdate', () => import('@/components/AppUpdate.vue'))
const AppBackupSettings = createAsyncRouteComponent(
  'AppBackupSettings',
  () => import('@/components/AppBackupSettings.vue'),
)
const AppBackupRecords = createAsyncRouteComponent(
  'AppBackupRecords',
  () => import('@/components/AppBackupRecords.vue'),
)
const AppBackupRestore = createAsyncRouteComponent(
  'AppBackupRestore',
  () => import('@/components/AppBackupRestore.vue'),
)
const AppProxySettings = createAsyncRouteComponent(
  'AppProxySettings',
  () => import('@/components/AppProxySettings.vue'),
)
const AppDatabaseRepair = createAsyncRouteComponent(
  'AppDatabaseRepair',
  () => import('@/components/AppDatabaseRepair.vue'),
)
const AppCloudDirSettings = createAsyncRouteComponent(
  'AppCloudDirSettings',
  () => import('@/components/cloud/CloudDirSettings.vue'),
)
const AppCloudSubscription = createAsyncRouteComponent(
  'AppCloudSubscription',
  () => import('@/components/cloud/CloudSubscription.vue'),
)
const AppCloudPlaceholder = createAsyncRouteComponent(
  'AppCloudPlaceholder',
  () => import('@/components/cloud/CloudPlaceholder.vue'),
)
const AppHiveSubscription = createAsyncRouteComponent(
  'AppHiveSubscription',
  () => import('@/components/cloud/HiveSubscription.vue'),
)
const AppHiveSettings = createAsyncRouteComponent(
  'AppHiveSettings',
  () => import('@/components/cloud/HiveSettings.vue'),
)
const AppDiscover = createAsyncRouteComponent('AppDiscover', () => import('@/components/AppDiscover.vue'))

// 定义路由元信息类型
declare module 'vue-router' {
  interface RouteMeta {
    title: string
    requiresAuth: boolean
    parent?: string
    icon?: string
    showInMenu?: boolean
  }
}

const routes = [
  {
    path: '/login',
    name: 'login',
    component: AppLogin,
    meta: {
      title: '登录',
      requiresAuth: false,
      showInMenu: false,
    },
  },
  {
    path: '/',
    name: 'home',
    component: AppHome,
    meta: {
      title: '首页',
      requiresAuth: true,
      icon: 'House',
      showInMenu: true,
    },
  },
  {
    path: '/logs',
    name: 'logs',
    component: AppLogPage,
    meta: {
      title: '运行日志',
      requiresAuth: true,
      icon: 'Document',
      showInMenu: true,
    },
  },
  {
    path: '/accounts',
    name: 'accounts',
    component: AppCloudAccounts,
    meta: {
      title: '网盘账号',
      requiresAuth: true,
      icon: 'User',
      showInMenu: true,
    },
  },
  {
    path: '/discover',
    name: 'discover',
    component: AppDiscover,
    meta: {
      title: '发现',
      requiresAuth: true,
      icon: 'Compass',
      showInMenu: true,
    },
  },
  {
    path: '/sync',
    name: 'sync',
    redirect: '/sync-directories',
    meta: {
      title: 'STRM 同步',
      requiresAuth: true,
      icon: 'DocumentCopy',
      showInMenu: true,
    },
  },
  {
    path: '/sync-directories',
    name: 'sync-directories',
    component: AppSyncDirectories,
    meta: {
      title: 'STRM 同步目录',
      requiresAuth: true,
      parent: 'sync',
      icon: 'FolderOpened',
      showInMenu: true,
    },
  },
  {
    path: '/sync-directory/add',
    name: 'sync-directory-add',
    component: AppSyncDirectoryForm,
    meta: {
      title: '添加同步目录',
      requiresAuth: true,
      parent: 'sync',
      showInMenu: false,
    },
  },
  {
    path: '/sync-directory/edit/:id',
    name: 'sync-directory-edit',
    component: AppSyncDirectoryForm,
    meta: {
      title: '编辑同步目录',
      requiresAuth: true,
      parent: 'sync',
      showInMenu: false,
    },
  },
  {
    path: '/sync-records',
    name: 'sync-records',
    component: AppSyncRecords,
    meta: {
      title: 'STRM 同步记录',
      requiresAuth: true,
      parent: 'sync',
      icon: 'List',
      showInMenu: true,
    },
  },
  {
    path: '/settings/strm',
    name: 'settings-strm',
    component: AppStrmSettings,
    meta: {
      title: 'STRM 设置',
      requiresAuth: true,
      parent: 'sync',
      icon: 'Setting',
      showInMenu: true,
    },
  },
  {
    path: '/sync-records/:id',
    name: 'sync-task-detail',
    component: AppSyncTaskDetail,
    meta: {
      title: '同步任务详情',
      requiresAuth: true,
      showInMenu: false,
    },
  },
  {
    path: '/scrape',
    name: 'scrape',
    redirect: '/scrape-pathes',
    meta: {
      title: '刮削 & 整理',
      requiresAuth: true,
      icon: 'Film',
      showInMenu: true,
    },
  },
  {
    path: '/scrape-pathes',
    name: 'scrape-pathes',
    component: AppScrapePathes,
    meta: {
      title: '刮削目录',
      requiresAuth: true,
      parent: 'scrape',
      icon: 'FolderOpened',
      showInMenu: true,
    },
  },
  {
    path: '/scrape-path/add',
    name: 'scrape-path-add',
    component: AppScrapePathForm,
    meta: {
      title: '添加刮削目录',
      requiresAuth: true,
      parent: 'scrape',
      showInMenu: false,
    },
  },
  {
    path: '/scrape-path/edit/:id',
    name: 'scrape-path-edit',
    component: AppScrapePathForm,
    meta: {
      title: '编辑刮削目录',
      requiresAuth: true,
      parent: 'scrape',
      showInMenu: false,
    },
  },
  {
    path: '/scrape-records',
    name: 'scrape-records',
    component: AppScrapeRecords,
    meta: {
      title: '刮削记录',
      requiresAuth: true,
      parent: 'scrape',
      icon: 'List',
      showInMenu: true,
    },
  },
  {
    path: '/settings/tmdb',
    name: 'settings-tmdb',
    component: AppTmdbSettings,
    meta: {
      title: '刮削设置',
      requiresAuth: true,
      parent: 'scrape',
      icon: 'Film',
      showInMenu: true,
    },
  },
  {
    path: '/settings/ai',
    name: 'settings-ai',
    component: AppAiSettings,
    meta: {
      title: 'AI 识别设置',
      requiresAuth: true,
      parent: 'scrape',
      icon: 'View',
      showInMenu: true,
    },
  },
  {
    path: '/settings/category-strategy',
    name: 'settings-category-strategy',
    component: AppCategoryStrategy,
    meta: {
      title: '二级分类设置',
      requiresAuth: true,
      parent: 'scrape',
      icon: 'Operation',
      showInMenu: true,
    },
  },

  {
    path: '/transfer',
    name: 'transfer',
    redirect: '/upload-queue',
    meta: {
      title: '上传下载',
      requiresAuth: true,
      icon: 'Download',
      showInMenu: true,
    },
  },
  {
    path: '/upload-queue',
    name: 'upload-queue',
    component: AppUploadQueue,
    meta: {
      title: '上传队列',
      requiresAuth: true,
      parent: 'transfer',
      icon: 'Upload',
      showInMenu: true,
    },
  },
  {
    path: '/download-queue',
    name: 'download-queue',
    component: AppDownloadQueue,
    meta: {
      title: '下载队列',
      requiresAuth: true,
      parent: 'transfer',
      icon: 'Download',
      showInMenu: true,
    },
  },
  {
    path: '/file-manager',
    name: 'file-manager',
    component: AppFileManager,
    meta: {
      title: '网盘文件管理',
      requiresAuth: true,
      icon: 'Folder',
      showInMenu: true,
    },
  },
  {
    path: '/database',
    name: 'database',
    component: AppBackupSettings,
    meta: {
      title: '备份恢复',
      requiresAuth: true,
      icon: 'DataAnalysis',
      showInMenu: true,
    },
  },
  {
    path: '/database/backup/settings',
    name: 'database-backup-settings',
    component: AppBackupSettings,
    meta: {
      title: '备份设置',
      requiresAuth: true,
      parent: 'database',
      showInMenu: true,
    },
  },
  {
    path: '/database/backup/records',
    name: 'database-backup-records',
    component: AppBackupRecords,
    meta: {
      title: '备份记录',
      requiresAuth: true,
      parent: 'database',
      icon: 'List',
      showInMenu: true,
    },
  },
  {
    path: '/database/backup/restore',
    name: 'database-backup-restore',
    component: AppBackupRestore,
    meta: {
      title: '备份恢复',
      requiresAuth: true,
      parent: 'database',
      icon: 'RefreshLeft',
      showInMenu: true,
    },
  },
  {
    path: '/settings',
    name: 'settings',
    component: AppUserSettings,
    meta: {
      title: '系统设置',
      requiresAuth: true,
      icon: 'Setting',
      showInMenu: true,
    },
  },
  {
    path: '/settings/user',
    name: 'settings-user',
    component: AppUserSettings,
    meta: {
      title: '用户管理',
      requiresAuth: true,
      parent: 'settings',
      icon: 'UserFilled',
      showInMenu: true,
    },
  },
  {
    path: '/settings/api-keys',
    name: 'settings-api-keys',
    component: AppApiKeys,
    meta: {
      title: 'API Key',
      requiresAuth: true,
      parent: 'settings',
      icon: 'Key',
      showInMenu: true,
    },
  },
  {
    path: '/settings/sessions',
    name: 'settings-sessions',
    component: AppLoginSessions,
    meta: {
      title: '登录设备',
      requiresAuth: true,
      parent: 'settings',
      icon: 'Monitor',
      showInMenu: true,
    },
  },
  {
    path: '/settings/notification',
    name: 'settings-notification',
    component: AppNotificationChannels,
    meta: {
      title: '通知管理',
      requiresAuth: true,
      parent: 'settings',
      icon: 'Promotion',
      showInMenu: true,
    },
  },
  {
    path: '/settings/moviepilot',
    redirect: '/moviepilot/settings',
  },
  {
    path: '/moviepilot',
    name: 'moviepilot',
    redirect: '/moviepilot/subscribes',
    meta: {
      title: '影视订阅',
      requiresAuth: true,
      icon: 'Collection',
      showInMenu: true,
    },
  },
  {
    path: '/moviepilot/subscribes',
    name: 'moviepilot-subscribes',
    component: AppMoviePilotSubscribes,
    meta: {
      title: '订阅管理',
      requiresAuth: true,
      parent: 'moviepilot',
      showInMenu: true,
    },
  },
  {
    path: '/moviepilot/settings',
    name: 'moviepilot-settings',
    component: AppMoviePilotSettings,
    meta: {
      title: '设置',
      requiresAuth: true,
      parent: 'moviepilot',
      showInMenu: true,
    },
  },
  {
    path: '/moviepilot/failed',
    name: 'moviepilot-failed',
    component: AppMoviePilotFailed,
    meta: {
      title: '识别失败',
      requiresAuth: true,
      parent: 'moviepilot',
      showInMenu: true,
    },
  },
  {
    path: '/settings/emby',
    name: 'settings-emby',
    component: AppEmbySettings,
    meta: {
      title: 'Emby',
      requiresAuth: true,
      parent: 'settings',
      icon: 'VideoPlay',
      showInMenu: true,
    },
  },
  {
    path: '/settings/threads',
    name: 'settings-threads',
    component: AppThreadSettings,
    meta: {
      title: '接口速率',
      requiresAuth: true,
      parent: 'settings',
      icon: 'Operation',
      showInMenu: true,
    },
  },
  {
    path: '/settings/log',
    name: 'settings-log',
    component: AppLogSettings,
    meta: {
      title: '日志设置',
      requiresAuth: true,
      parent: 'settings',
      icon: 'List',
      showInMenu: true,
    },
  },
  {
    path: '/proxy',
    name: 'proxy',
    component: AppProxySettings,
    meta: {
      title: '网络代理',
      requiresAuth: true,
      parent: 'settings',
      icon: 'Link',
      showInMenu: true,
    },
  },
  {
    path: '/settings/update',
    name: 'settings-update',
    component: AppUpdate,
    meta: {
      title: '版本更新',
      requiresAuth: true,
      parent: 'settings',
      icon: 'Upload',
      showInMenu: true,
    },
  },
  {
    path: '/settings/database-repair',
    name: 'settings-database-repair',
    component: AppDatabaseRepair,
    meta: {
      title: '数据库修复',
      requiresAuth: true,
      parent: 'settings',
      icon: 'DataLine',
      showInMenu: true,
    },
  },
  {
    path: '/cloud-123',
    name: 'cloud-123',
    redirect: '/cloud-123/dir-settings',
    meta: {
      title: '123 云盘',
      requiresAuth: true,
      icon: 'Folder',
      showInMenu: true,
    },
  },
  {
    path: '/cloud-123/dir-settings',
    name: 'cloud-123-dir-settings',
    component: AppCloudDirSettings,
    props: { sourceType: '123', sourceName: '123 云盘' },
    meta: {
      title: '转存目录设置',
      requiresAuth: true,
      parent: 'cloud-123',
      icon: 'Setting',
      showInMenu: true,
    },
  },
  {
    path: '/cloud-123/subscriptions',
    name: 'cloud-123-subscriptions',
    component: AppCloudSubscription,
    props: { sourceType: '123', sourceName: '123 云盘' },
    meta: {
      title: '频道订阅',
      requiresAuth: true,
      parent: 'cloud-123',
      icon: 'Link',
      showInMenu: true,
    },
  },
  {
    path: '/cloud-123/tasks',
    name: 'cloud-123-tasks',
    component: AppCloudPlaceholder,
    props: {
      sourceName: '123 云盘',
      title: '转存任务',
      description: '该功能开发中。当前转存任务可前往「传输」页面查看下载队列，或通过 Telegram 发送分享链接直接转存。',
      related: [{ name: '下载队列', path: '/download-queue' }],
    },
    meta: {
      title: '转存任务',
      requiresAuth: true,
      parent: 'cloud-123',
      icon: 'List',
      showInMenu: true,
    },
  },
  {
    path: '/cloud-123/organize',
    name: 'cloud-123-organize',
    component: AppCloudPlaceholder,
    props: {
      sourceName: '123 云盘',
      title: '自动整理分类',
      description: '该功能开发中。后续将支持按规则自动整理转存目录中的文件。',
      related: [],
    },
    meta: {
      title: '自动整理分类',
      requiresAuth: true,
      parent: 'cloud-123',
      icon: 'Operation',
      showInMenu: true,
    },
  },
  {
    path: '/cloud-123/scrape',
    name: 'cloud-123-scrape',
    component: AppCloudPlaceholder,
    props: {
      sourceName: '123 云盘',
      title: 'MP 刮削推送',
      description: '该功能开发中。后续将支持转存后自动刮削并推送到 MoviePilot / Emby。',
      related: [{ name: '刮削记录', path: '/scrape-records' }],
    },
    meta: {
      title: 'MP 刮削推送',
      requiresAuth: true,
      parent: 'cloud-123',
      icon: 'Film',
      showInMenu: true,
    },
  },
  {
    path: '/cloud-guangyapan',
    name: 'cloud-guangyapan',
    redirect: '/cloud-guangyapan/dir-settings',
    meta: {
      title: '光鸭云盘',
      requiresAuth: true,
      icon: 'FolderOpened',
      showInMenu: true,
    },
  },
  {
    path: '/cloud-guangyapan/dir-settings',
    name: 'cloud-guangyapan-dir-settings',
    component: AppCloudDirSettings,
    props: { sourceType: 'guangyapan', sourceName: '光鸭云盘' },
    meta: {
      title: '转存目录设置',
      requiresAuth: true,
      parent: 'cloud-guangyapan',
      icon: 'Setting',
      showInMenu: true,
    },
  },
  {
    path: '/cloud-guangyapan/subscriptions',
    name: 'cloud-guangyapan-subscriptions',
    component: AppCloudSubscription,
    props: { sourceType: 'guangyapan', sourceName: '光鸭云盘' },
    meta: {
      title: '频道订阅',
      requiresAuth: true,
      parent: 'cloud-guangyapan',
      icon: 'Link',
      showInMenu: true,
    },
  },
  {
    path: '/cloud-guangyapan/tasks',
    name: 'cloud-guangyapan-tasks',
    component: AppCloudPlaceholder,
    props: {
      sourceName: '光鸭云盘',
      title: '转存任务',
      description: '该功能开发中。当前转存任务可前往「传输」页面查看下载队列，或通过 Telegram 发送分享链接直接转存。',
      related: [{ name: '下载队列', path: '/download-queue' }],
    },
    meta: {
      title: '转存任务',
      requiresAuth: true,
      parent: 'cloud-guangyapan',
      icon: 'List',
      showInMenu: true,
    },
  },
  {
    path: '/cloud-guangyapan/organize',
    name: 'cloud-guangyapan-organize',
    component: AppCloudPlaceholder,
    props: {
      sourceName: '光鸭云盘',
      title: '自动整理分类',
      description: '该功能开发中。后续将支持按规则自动整理转存目录中的文件。',
      related: [],
    },
    meta: {
      title: '自动整理分类',
      requiresAuth: true,
      parent: 'cloud-guangyapan',
      icon: 'Operation',
      showInMenu: true,
    },
  },
  {
    path: '/cloud-guangyapan/scrape',
    name: 'cloud-guangyapan-scrape',
    component: AppCloudPlaceholder,
    props: {
      sourceName: '光鸭云盘',
      title: 'MP 刮削推送',
      description: '该功能开发中。后续将支持转存后自动刮削并推送到 MoviePilot / Emby。',
      related: [{ name: '刮削记录', path: '/scrape-records' }],
    },
    meta: {
      title: 'MP 刮削推送',
      requiresAuth: true,
      parent: 'cloud-guangyapan',
      icon: 'Film',
      showInMenu: true,
    },
  },
  {
    path: '/cloud-pan139',
    name: 'cloud-pan139',
    redirect: '/cloud-pan139/dir-settings',
    meta: {
      title: '移动云盘',
      requiresAuth: true,
      icon: 'Promotion',
      showInMenu: true,
    },
  },
  {
    path: '/cloud-pan139/dir-settings',
    name: 'cloud-pan139-dir-settings',
    component: AppCloudDirSettings,
    props: { sourceType: 'pan139', sourceName: '移动云盘' },
    meta: {
      title: '转存目录设置',
      requiresAuth: true,
      parent: 'cloud-pan139',
      icon: 'Setting',
      showInMenu: true,
    },
  },
  {
    path: '/cloud-pan139/subscriptions',
    name: 'cloud-pan139-subscriptions',
    component: AppCloudSubscription,
    props: { sourceType: 'pan139', sourceName: '移动云盘' },
    meta: {
      title: '频道订阅',
      requiresAuth: true,
      parent: 'cloud-pan139',
      icon: 'Link',
      showInMenu: true,
    },
  },
  {
    path: '/cloud-pan139/tasks',
    name: 'cloud-pan139-tasks',
    component: AppCloudPlaceholder,
    props: {
      sourceName: '移动云盘',
      title: '转存任务',
      description: '该功能开发中。当前转存任务可前往「传输」页面查看下载队列，或通过 Telegram 发送分享链接直接转存。',
      related: [{ name: '下载队列', path: '/download-queue' }],
    },
    meta: {
      title: '转存任务',
      requiresAuth: true,
      parent: 'cloud-pan139',
      icon: 'List',
      showInMenu: true,
    },
  },
  {
    path: '/cloud-pan139/organize',
    name: 'cloud-pan139-organize',
    component: AppCloudPlaceholder,
    props: {
      sourceName: '移动云盘',
      title: '自动整理分类',
      description: '该功能开发中。后续将支持按规则自动整理转存目录中的文件。',
      related: [],
    },
    meta: {
      title: '自动整理分类',
      requiresAuth: true,
      parent: 'cloud-pan139',
      icon: 'Operation',
      showInMenu: true,
    },
  },
  {
    path: '/cloud-pan139/scrape',
    name: 'cloud-pan139-scrape',
    component: AppCloudPlaceholder,
    props: {
      sourceName: '移动云盘',
      title: 'MP 刮削推送',
      description: '该功能开发中。后续将支持转存后自动刮削并推送到 MoviePilot / Emby。',
      related: [{ name: '刮削记录', path: '/scrape-records' }],
    },
    meta: {
      title: 'MP 刮削推送',
      requiresAuth: true,
      parent: 'cloud-pan139',
      icon: 'Film',
      showInMenu: true,
    },
  },
  {
    path: '/cloud-hdhive',
    name: 'cloud-hdhive',
    redirect: '/cloud-hdhive/subscriptions',
    meta: {
      title: '影巢订阅',
      requiresAuth: true,
      icon: 'Link',
      showInMenu: true,
    },
  },
  {
    path: '/cloud-hdhive/subscriptions',
    name: 'cloud-hdhive-subscriptions',
    component: AppHiveSubscription,
    meta: {
      title: '资源订阅',
      requiresAuth: true,
      parent: 'cloud-hdhive',
      icon: 'Link',
      showInMenu: true,
    },
  },
  {
    path: '/cloud-hdhive/settings',
    name: 'cloud-hdhive-settings',
    component: AppHiveSettings,
    meta: {
      title: '影巢设置',
      requiresAuth: true,
      parent: 'cloud-hdhive',
      icon: 'Setting',
      showInMenu: true,
    },
  },
]

const router = createRouter({
  history: createQMediaSyncHashHistory(),
  routes,
})

// 路由守卫
router.beforeEach(async (to) => {
  const authStore = useAuthStore()

  if (!authStore.hasInitialized && authStore.authStatus === 'checking') {
    await authStore.bootstrapAuth(http)
  }

  if (to.meta.requiresAuth && !authStore.isAuthenticated) {
    return {
      name: 'login',
      query: { redirect: to.fullPath },
      replace: true,
    }
  }

  if (to.name === 'login' && authStore.isAuthenticated) {
    return { name: 'home', replace: true }
  }

  return true
})

router.afterEach((to, _from, failure) => {
  if (!failure && to.meta.title) {
    document.title = `${to.meta.title} - QMediaSync`
  }
})

export default router
