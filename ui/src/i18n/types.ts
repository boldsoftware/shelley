export type Locale = "en" | "ja" | "fr" | "ru" | "es" | "zh-CN" | "zh-TW" | "upgoer5";

export interface TranslationKeys {
  // App-level
  loading: string;
  retry: string;
  failedToLoadConversations: string;

  // Chat Header & Actions
  newConversation: string;
  moreOptions: string;
  conversations: string;

  // Overflow Menu
  diffs: string;
  terminal: string;
  archiveConversation: string;
  checkForNewVersion: string;
  markdown: string;
  off: string;
  agent: string;
  all: string;

  // Theme
  system: string;
  light: string;
  dark: string;

  // Notifications
  enableNotifications: string;
  disableNotifications: string;
  blockedByBrowser: string;
  osNotificationsWhenHidden: string;
  requiresBrowserPermission: string;
  on: string;

  // Command Palette
  searchPlaceholder: string;
  searching: string;
  noResults: string;
  toNavigate: string;
  toSelect: string;
  toClose: string;
  action: string;

  // Command Palette Actions
  newConversationAction: string;
  startNewConversation: string;
  nextConversation: string;
  switchToNext: string;
  previousConversation: string;
  switchToPrevious: string;
  nextUserMessage: string;
  jumpToNextMessage: string;
  previousUserMessage: string;
  jumpToPreviousMessage: string;
  viewDiffs: string;
  openGitDiffViewer: string;
  addRemoveModelsKeys: string;
  configureModels: string;
  notificationSettings: string;
  configureNotifications: string;
  enableMarkdownAgent: string;
  renderMarkdownAgent: string;
  enableMarkdownAll: string;
  renderMarkdownAll: string;
  disableMarkdown: string;
  showPlainText: string;
  archiveConversationAction: string;
  archiveCurrentConversation: string;
  newConversationInMainRepo: string;
  newConversationInNewWorktree: string;
  createNewWorktree: string;

  // Conversation Drawer
  archived: string;
  noArchivedConversations: string;
  noConversationsYet: string;
  startNewToGetStarted: string;
  backToConversations: string;
  viewArchived: string;
  rename: string;
  archive: string;
  restore: string;
  deletePermanently: string;
  confirmDelete: string;
  duplicateName: string;
  agentIsWorking: string;
  subagentIsWorking: string;
  hideSubagents: string;
  showSubagents: string;
  groupConversations: string;
  noGrouping: string;
  directory: string;
  gitRepo: string;
  other: string;
  collapseSubagents: string;
  expandSubagents: string;
  collapseSidebar: string;
  closeConversations: string;
  yesterday: string;
  daysAgo: string;

  // Message Input
  messagePlaceholder: string;
  messagePlaceholderShort: string;
  attachFile: string;
  sendMessage: string;
  startVoiceInput: string;
  stopVoiceInput: string;
  dropFilesHere: string;
  uploading: string;
  uploadFailed: string;

  // Models Modal
  manageModels: string;
  addModel: string;
  editModel: string;
  loadingModels: string;
  providerApiFormat: string;
  endpoint: string;
  defaultEndpoint: string;
  customEndpoint: string;
  model: string;
  displayName: string;
  nameShownInSelector: string;
  apiKey: string;
  enterApiKey: string;
  maxContextTokens: string;
  tags: string;
  tagsPlaceholder: string;
  tagsTooltip: string;
  testButton: string;
  testingButton: string;
  save: string;
  cancel: string;
  duplicate: string;
  delete_: string;
  modelNameRequired: string;
  apiKeyRequired: string;
  noModelsConfigured: string;
  noModelsHint: string;

  // Notifications Modal
  notifications: string;
  browserNotifications: string;
  faviconBadge: string;
  editChannel: string;
  addChannel: string;
  customChannels: string;
  noCustomChannels: string;
  addWebhookHint: string;
  channelName: string;
  channelType: string;
  webhookUrl: string;
  enabled: string;
  testNotification: string;
  denied: string;
  noServerChannelsConfigured: string;
  addOne: string;
  edit: string;

  // Diff Viewer
  noFiles: string;
  chooseFile: string;
  commentMode: string;
  editMode: string;

  // Directory Picker
  newFolderName: string;
  create: string;
  noMatchingDirectories: string;
  noSubdirectories: string;
  createNewFolder: string;

  // Messages
  copyCommitHash: string;
  clickToCopyCommitHash: string;
  unknownTool: string;
  toolOutput: string;
  errorOccurred: string;

  // Version
  updateAvailable: string;

  // Welcome / Empty State
  welcomeTitle: string;
  welcomeSubtitle: string;
  welcomeMessage: string;
  sendMessageToStart: string;
  noModelsConfiguredHint: string;

  // Status Bar
  modelLabel: string;
  dirLabel: string;

  // Sidebar buttons
  openConversations: string;
  expandSidebar: string;

  // Language
  language: string;
  switchLanguage: string;
  reportBug: string;
  english: string;
  japanese: string;
  french: string;
  russian: string;
  spanish: string;
  upgoerFive: string;
  simplifiedChinese: string;
  traditionalChinese: string;
}
