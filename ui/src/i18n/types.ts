export type Locale = "en" | "ja" | "fr" | "ru" | "es" | "zh-CN" | "zh-TW" | "upgoer5" | "vi";

export interface TranslationKeys {
  // App-level
  loading: string;
  retry: string;
  failedToLoadConversations: string;

  // Chat Header & Actions
  newConversation: string;
  moreOptions: string;
  actions: string;
  conversations: string;

  // Overflow Menu
  diffs: string;
  gitGraph: string;
  terminal: string;
  archiveConversation: string;
  exportConversation: string;
  checkForNewVersion: string;
  conversationView: string;
  seeAllMessages: string;
  seeEndOfTurnMessagesOnly: string;
  brevity: string;
  look: string;

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
  searchConversations: string;
  noSearchResults: string;
  clearSearch: string;
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
  openGitGraphViewer: string;
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
  newConversationInHomeDirectory: string;
  newConversationInNewWorktree: string;
  createNewWorktree: string;
  setWorkingDirToRepoRoot: string;
  setWorkingDirToMainRepo: string;

  // Conversation Drawer
  archived: string;
  noArchivedConversations: string;
  noConversationsYet: string;
  startNewToGetStarted: string;
  backToConversations: string;
  viewArchived: string;
  rename: string;
  editTags: string;
  addTagPlaceholder: string;
  removeTag: string;
  addTag: string;
  archive: string;
  restore: string;
  deletePermanently: string;
  confirmDelete: string;
  confirmDeleteShort: string;
  duplicateName: string;
  agentIsWorking: string;
  subagentIsWorking: string;
  running: string;
  hideSubagents: string;
  terminalsPinnedHere: string;
  showSubagents: string;
  groupConversations: string;
  resortNow: string;
  noGrouping: string;
  directory: string;
  gitRepo: string;
  other: string;
  clearTagFilter: string;
  untagged: string;
  searchOrTagPlaceholder: string;
  noMatchingTags: string;
  noTagsToNarrow: string;
  noConversationsMatchTags: string;
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
  refreshModels: string;
  refreshingModels: string;
  searchModels: string;
  noModelsFound: string;
  notReadyBadge: string;
  showAllModels: string;
  showFewerModels: string;
  manageModelsAction: string;
  effortLabel: string;
  effortAuto: string;
  modelSwitchBusy: string;
  modelSwitchHint: string;
  cwdChangeHint: string;
  cwdChangeBusy: string;
  customModelsGroup: string;
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
  imageSupport: string;
  imageSupportHelp: string;
  imageSupportAuto: string;
  imageSupportYes: string;
  imageSupportNo: string;
  tags: string;
  tagsPlaceholder: string;
  tagsTooltip: string;
  columnName: string;
  columnModelId: string;
  columnProvider: string;
  columnActions: string;
  columnImages: string;
  imageSupportAutoShort: string;
  imageSupportAutoResolved: string;
  reasoningEffort: string;
  reasoningEffortPlaceholder: string;
  reasoningEffortHint: string;
  supportsReasoning: string;
  reasoningSupportAuto: string;
  reasoningSupportYes: string;
  reasoningSupportNo: string;
  reasoningSupportHelp: string;
  reasoningLevelMapping: string;
  reasoningMappingUnsupported: string;
  reasoningMappingHelp: string;
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
  exeDevPushNotifications: string;
  exeDevPushNotificationsDescription: string;
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
  // welcomeMessageLocal is shown instead of welcomeMessage when Shelley is not
  // running on an exe.dev host, so it omits exe.dev-specific proxy details.
  welcomeMessageLocal: string;
  // Link labels embedded in the welcome messages via {openSourceLink} and
  // {customizeLink} placeholders.
  welcomeOpenSource: string;
  welcomeCustomize: string;
  sendMessageToStart: string;
  noModelsTitle: string;
  noModelsExeNote: string;
  noModelsLocalNote: string;
  noModelsExeRefresh: string;
  noModelSelectedPlaceholder: string;

  // Status Bar
  dirLabel: string;

  // AGENTS.md editor
  editUserAgentsMd: string;
  editFile: string;
  editFileShortcut: string;
  editFileShortcutFirefox: string;

  // Sidebar buttons
  openConversations: string;
  commandMenu: string;
  expandSidebar: string;

  // Language
  language: string;
  off: string;
  switchLanguage: string;
  english: string;
  japanese: string;
  french: string;
  russian: string;
  spanish: string;
  upgoerFive: string;
  simplifiedChinese: string;
  traditionalChinese: string;
  vietnamese: string;
}
