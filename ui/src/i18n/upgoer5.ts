import type { TranslationKeys } from "./types";

export const upgoer5: TranslationKeys = {
  // App-level
  loading: "Getting ready...",
  retry: "Try again",
  failedToLoadConversations: "Could not get your talks. Please open this again.",

  // Chat Header & Actions
  newConversation: "New Talk",
  moreOptions: "More things to do",
  conversations: "Talks",

  // Overflow Menu
  diffs: "Changes",
  terminal: "Computer Window",
  archiveConversation: "Put Away Talk",
  checkForNewVersion: "Look for a newer one",
  markdown: "Pretty Words",
  off: "Off",
  agent: "Helper",
  all: "All",

  // Theme
  system: "Same as computer",
  light: "Light",
  dark: "Dark",

  // Notifications
  enableNotifications: "Tell me when things happen",
  disableNotifications: "Don't tell me when things happen",
  blockedByBrowser: "Your window thing said no",
  osNotificationsWhenHidden: "Tell you things even when you can't see this",
  requiresBrowserPermission: "You need to say okay first",
  on: "On",

  // Command Palette
  searchPlaceholder: "Look for talks or things to do...",
  searching: "Looking...",
  noResults: "Nothing found",
  toNavigate: "to move around",
  toSelect: "to pick",
  toClose: "to close",
  action: "Do it",

  // Command Palette Actions
  newConversationAction: "New Talk",
  startNewConversation: "Start a new talk",
  nextConversation: "Next Talk",
  switchToNext: "Go to the next talk",
  previousConversation: "Talk Before This",
  switchToPrevious: "Go to the talk before this one",
  nextUserMessage: "Next Thing You Said",
  jumpToNextMessage: "Jump to the next thing you said",
  previousUserMessage: "Last Thing You Said",
  jumpToPreviousMessage: "Jump to the thing you said before",
  viewDiffs: "See Changes",
  openGitDiffViewer: "Open the thing that shows what changed",
  addRemoveModelsKeys: "Set Up Brains and Keys",
  configureModels: "Pick which brains to use and give them keys",
  notificationSettings: "How to Tell You Things",
  configureNotifications: "Change how you hear about stuff",
  enableMarkdownAgent: "Pretty Words (Helper)",
  renderMarkdownAgent: "Make helper words look pretty",
  enableMarkdownAll: "Pretty Words (All)",
  renderMarkdownAll: "Make all words look pretty",
  disableMarkdown: "No Pretty Words",
  showPlainText: "Show just the words for everything",
  archiveConversationAction: "Put Away Talk",
  archiveCurrentConversation: "Put away the talk you are in",
  newConversationInMainRepo: "New Talk in Home Place",
  newConversationInNewWorktree: "New Talk in New Work Place",
  createNewWorktree: "Make a new work place for this talk",

  // Conversation Drawer
  archived: "Put Away",
  noArchivedConversations: "No talks put away",
  noConversationsYet: "No talks yet",
  startNewToGetStarted: "Start a new talk to get going",
  backToConversations: "Back to talks",
  viewArchived: "See Put Away",
  rename: "Give New Name",
  archive: "Put Away",
  restore: "Bring Back",
  deletePermanently: "Throw Away for Good",
  confirmDelete: "Are you sure? You can't take this back.",
  duplicateName: "There is already a talk with that name",
  agentIsWorking: "Helper is working...",
  subagentIsWorking: "Little helper is working...",
  hideSubagents: "Hide little helpers",
  showSubagents: "Show little helpers",
  groupConversations: "Put talks in groups",
  noGrouping: "No groups",
  directory: "Place",
  gitRepo: "Where Your Work Lives",
  other: "Other",
  collapseSubagents: "Close up little helpers",
  expandSubagents: "Open up little helpers",
  collapseSidebar: "Make side part small",
  closeConversations: "Close talks",
  yesterday: "Before Today",
  daysAgo: "days ago",

  // Message Input
  messagePlaceholder: "Write something, drop in a picture, or add a thing...",
  messagePlaceholderShort: "Write...",
  attachFile: "Add a thing",
  sendMessage: "Send it",
  startVoiceInput: "Start using your voice",
  stopVoiceInput: "Stop using your voice",
  dropFilesHere: "Drop stuff here",
  uploading: "Sending up...",
  uploadFailed: "Could not send it up",

  // Models Modal
  manageModels: "Set Up Brains",
  addModel: "Add Brain",
  editModel: "Change Brain",
  loadingModels: "Getting brains...",
  providerApiFormat: "Who Made It / How to Talk to It",
  endpoint: "Where to Send Stuff",
  defaultEndpoint: "The usual place",
  customEndpoint: "A different place",
  model: "Brain",
  displayName: "Name to Show",
  nameShownInSelector: "Name that shows up when you pick one",
  apiKey: "Key",
  enterApiKey: "Put in your key",
  maxContextTokens: "Most words it can hold in its head",
  tags: "Marks",
  tagsPlaceholder: "put a small low mark between each one, like: fast, big",
  tagsTooltip:
    "Marks for this brain, each one after the other with a small low mark between. You can use a mark to make this brain be the one that writes short names for your talks. If you do not put that mark on any brain, the brain you are using right now will make the name all on its own.",
  testButton: "Try It",
  testingButton: "Trying...",
  save: "Save",
  cancel: "Never Mind",
  duplicate: "Make Another",
  delete_: "Throw Away",
  modelNameRequired: "The brain needs a name",
  apiKeyRequired: "You need to put in a key",
  noModelsConfigured: "No brains set up",
  noModelsHint:
    "Set the right words where the computer looks for them, or use a way in, or add one down here.",

  // Notifications Modal
  notifications: "Things to Tell You",
  browserNotifications: "Pop ups to tell you",
  faviconBadge: "Little Mark on the Small Picture",
  editChannel: "Change a Way to Hear",
  addChannel: "Add a Way to Hear",
  customChannels: "Your Own Ways to Hear",
  noCustomChannels: "No ways to hear set up yet",
  addWebhookHint: "Add a place to hear about things",
  channelName: "What to Call It",
  channelType: "What Kind",
  webhookUrl: "Where to Send It",
  enabled: "On",
  testNotification: "Try Telling You",
  denied: "Said No",
  noServerChannelsConfigured: "No ways to hear from the far computer set up",
  addOne: "Add one",
  edit: "Change",

  // Diff Viewer
  noFiles: "No things",
  chooseFile: "Pick a thing to see what changed",
  commentMode: "Talk About It",
  editMode: "Change It",

  // Directory Picker
  newFolderName: "New place name",
  create: "Make",
  noMatchingDirectories: "No places like that",
  noSubdirectories: "No smaller places inside",
  createNewFolder: "Make a new place",

  // Messages
  copyCommitHash: "Take the save number",
  clickToCopyCommitHash: "Press to take the save number",
  unknownTool: "Thing we don't know",
  toolOutput: "What It Said",
  errorOccurred: "Something went wrong",

  // Version
  updateAvailable: "There is a newer one",

  // Welcome / Empty State
  welcomeTitle: "Your Helper",
  welcomeSubtitle: "",
  welcomeMessage:
    "This is a helper, running on {hostname}. You can ask it to do stuff. If you build something for people to see, you can use the pass through (see {docsLink}) to visit it at {proxyLink}.",
  sendMessageToStart: "Send a note to start talking.",
  noModelsConfiguredHint: "No brains set up yet. You need to add one to get started.",

  // Status Bar
  modelLabel: "Brain:",
  dirLabel: "Place:",

  // Sidebar buttons
  openConversations: "Open talks",
  expandSidebar: "Make side bigger",

  // Language
  language: "Words",
  switchLanguage: "Change words",
  reportBug: "Tell us something is broken",
  english: "English",
  japanese: "日本語",
  french: "Français",
  russian: "Русский",
  spanish: "Español",
  upgoerFive: "Up-Goer Five",
  simplifiedChinese: "简体中文",
  traditionalChinese: "繁體中文",
};
