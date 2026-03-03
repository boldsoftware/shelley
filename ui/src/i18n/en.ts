import type { TranslationKeys } from "./types";

export const en: TranslationKeys = {
  // App-level
  loading: "Loading...",
  retry: "Retry",
  failedToLoadConversations: "Failed to load conversations. Please refresh the page.",

  // Chat Header & Actions
  newConversation: "New Conversation",
  moreOptions: "More options",
  conversations: "Conversations",

  // Overflow Menu
  diffs: "Diffs",
  terminal: "Terminal",
  archiveConversation: "Archive Conversation",
  checkForNewVersion: "Check for New Version",
  markdown: "Markdown",
  off: "Off",
  agent: "Agent",
  all: "All",

  // Theme
  system: "System",
  light: "Light",
  dark: "Dark",

  // Notifications
  enableNotifications: "Enable Notifications",
  disableNotifications: "Disable Notifications",
  blockedByBrowser: "Blocked by browser",
  osNotificationsWhenHidden: "OS notifications when tab is hidden",
  requiresBrowserPermission: "Requires browser permission",
  on: "On",

  // Command Palette
  searchPlaceholder: "Search conversations or actions...",
  searching: "Searching...",
  noResults: "No results found",
  toNavigate: "to navigate",
  toSelect: "to select",
  toClose: "to close",
  action: "Action",

  // Command Palette Actions
  newConversationAction: "New Conversation",
  startNewConversation: "Start a new conversation",
  nextConversation: "Next Conversation",
  switchToNext: "Switch to the next conversation",
  previousConversation: "Previous Conversation",
  switchToPrevious: "Switch to the previous conversation",
  nextUserMessage: "Next User Message",
  jumpToNextMessage: "Jump to the next user message",
  previousUserMessage: "Previous User Message",
  jumpToPreviousMessage: "Jump to the previous user message",
  viewDiffs: "View Diffs",
  openGitDiffViewer: "Open the git diff viewer",
  addRemoveModelsKeys: "Add/Remove Models & Keys",
  configureModels: "Configure AI models and API keys",
  notificationSettings: "Notification Settings",
  configureNotifications: "Configure notification preferences",
  enableMarkdownAgent: "Enable Markdown (Agent)",
  renderMarkdownAgent: "Render markdown for agent messages",
  enableMarkdownAll: "Enable Markdown (All)",
  renderMarkdownAll: "Render markdown for all messages",
  disableMarkdown: "Disable Markdown",
  showPlainText: "Show plain text for all messages",
  archiveConversationAction: "Archive Conversation",
  archiveCurrentConversation: "Archive the current conversation",
  newConversationInMainRepo: "New Conversation in Main Repo",
  newConversationInNewWorktree: "New Conversation in New Worktree",
  createNewWorktree: "Create a new git worktree for this conversation",

  // Conversation Drawer
  archived: "Archived",
  noArchivedConversations: "No archived conversations",
  noConversationsYet: "No conversations yet",
  startNewToGetStarted: "Start a new conversation to get started",
  backToConversations: "Back to conversations",
  viewArchived: "View Archived",
  rename: "Rename",
  archive: "Archive",
  restore: "Restore",
  deletePermanently: "Delete Permanently",
  confirmDelete: "Are you sure? This cannot be undone.",
  duplicateName: "A conversation with this name already exists",
  agentIsWorking: "Agent is working...",
  subagentIsWorking: "Subagent is working...",
  hideSubagents: "Hide subagents",
  showSubagents: "Show subagents",
  groupConversations: "Group conversations",
  noGrouping: "No grouping",
  directory: "Directory",
  gitRepo: "Git Repo",
  other: "Other",
  collapseSubagents: "Collapse subagents",
  expandSubagents: "Expand subagents",
  collapseSidebar: "Collapse sidebar",
  closeConversations: "Close conversations",
  yesterday: "Yesterday",
  daysAgo: "days ago",

  // Message Input
  messagePlaceholder: "Message, paste image, or attach file...",
  messagePlaceholderShort: "Message...",
  attachFile: "Attach file",
  sendMessage: "Send message",
  startVoiceInput: "Start voice input",
  stopVoiceInput: "Stop voice input",
  dropFilesHere: "Drop files here",
  uploading: "Uploading...",
  uploadFailed: "Upload failed",

  // Models Modal
  manageModels: "Manage Models",
  addModel: "Add Model",
  editModel: "Edit Model",
  loadingModels: "Loading models...",
  providerApiFormat: "Provider / API Format",
  endpoint: "Endpoint",
  defaultEndpoint: "Default endpoint",
  customEndpoint: "Custom endpoint",
  model: "Model",
  displayName: "Display Name",
  nameShownInSelector: "Name shown in the model selector",
  apiKey: "API Key",
  enterApiKey: "Enter API key",
  maxContextTokens: "Max Context Tokens",
  tags: "Tags",
  tagsPlaceholder: "comma-separated, e.g., slug, cheap",
  tagsTooltip:
    'Comma-separated tags for this model. Use "slug" to mark this model for generating conversation titles. If no model has the "slug" tag, the conversation\'s model will be used.',
  testButton: "Test",
  testingButton: "Testing...",
  save: "Save",
  cancel: "Cancel",
  duplicate: "Duplicate",
  delete_: "Delete",
  modelNameRequired: "Model name is required",
  apiKeyRequired: "API key is required",
  noModelsConfigured: "No models configured",
  noModelsHint:
    "Set environment variables like ANTHROPIC_API_KEY, or use the -gateway flag, or add a custom model below.",

  // Notifications Modal
  notifications: "Notifications",
  browserNotifications: "Browser Notifications",
  faviconBadge: "Favicon Badge",
  editChannel: "Edit Channel",
  addChannel: "Add Channel",
  customChannels: "Custom Channels",
  noCustomChannels: "No custom channels configured",
  addWebhookHint: "Add a webhook URL to receive notifications",
  channelName: "Channel Name",
  channelType: "Channel Type",
  webhookUrl: "Webhook URL",
  enabled: "Enabled",
  testNotification: "Test Notification",
  denied: "Denied",
  noServerChannelsConfigured: "No server channels configured",
  addOne: "Add one",
  edit: "Edit",

  // Diff Viewer
  noFiles: "No files",
  chooseFile: "Choose a file to view diffs",
  commentMode: "Comment Mode",
  editMode: "Edit Mode",

  // Directory Picker
  newFolderName: "New folder name",
  create: "Create",
  noMatchingDirectories: "No matching directories",
  noSubdirectories: "No subdirectories",
  createNewFolder: "Create new folder",

  // Messages
  copyCommitHash: "Copy commit hash",
  clickToCopyCommitHash: "Click to copy commit hash",
  unknownTool: "Unknown tool",
  toolOutput: "Tool Output",
  errorOccurred: "An error occurred",

  // Version
  updateAvailable: "Update available",

  // Welcome / Empty State
  welcomeTitle: "Shelley Agent",
  welcomeSubtitle: "",
  welcomeMessage:
    "Shelley is an agent, running on {hostname}. You can ask Shelley to do stuff. If you build a web site with Shelley, you can use exe.dev's proxy features (see {docsLink}) to visit it over the web at {proxyLink}.",
  sendMessageToStart: "Send a message to start the conversation.",
  noModelsConfiguredHint: "No AI models configured. Press Ctrl+K or ⌘+K to add a model.",

  // Status Bar
  modelLabel: "Model:",
  dirLabel: "Dir:",

  // Sidebar buttons
  openConversations: "Open conversations",
  expandSidebar: "Expand sidebar",

  // Language
  language: "Language",
  switchLanguage: "Switch language",
  english: "English",
  japanese: "日本語",
  french: "Français",
  russian: "Русский",
  spanish: "Español",
};
