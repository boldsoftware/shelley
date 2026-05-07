import type { TranslationKeys } from "./types";

export const vi: TranslationKeys = {
  // App-level
  loading: "Đang tải ...",
  retry: "Thử lại",
  failedToLoadConversations: "Tải trò chuyện thất bại. Vui lòng tải lại trang.",

  // Chat Header & Actions
  newConversation: "Hội thoại mới",
  moreOptions: "Tùy chọn khác",
  conversations: "Lịch sử",

  // Overflow Menu
  diffs: "Thay đổi",
  gitGraph: "Biểu đồ Git",
  terminal: "Terminal",
  archiveConversation: "Hội thoại đã lưu trữ",
  checkForNewVersion: "Kiểm tra cập nhật",
  markdown: "Markdown",
  off: "Tắt",
  agent: "Agent",
  all: "Tất cả",

  // Theme
  system: "Hệ thống",
  light: "Ngày",
  dark: "Đêm",

  // Notifications
  enableNotifications: "Bật thông báo",
  disableNotifications: "Tắt thông báo",
  blockedByBrowser: "Bị trình duyệt chặn",
  osNotificationsWhenHidden: "Gửi thông báo khi ẩn tab",
  requiresBrowserPermission: "Yêu cầu cấp quyền",
  on: "Bật",

  // Command Palette
  searchPlaceholder: "Tìm hội thoại hoặc hành động",
  searching: "Đang tìm...",
  noResults: "Không tìm thấy",
  toNavigate: "để điều hướng",
  toSelect: "để chọn",
  toClose: "để đóng",
  action: "Hành động",

  // Command Palette Actions
  newConversationAction: "Hội thoại mới",
  startNewConversation: "Bắt đầu hội thoại mới",
  nextConversation: "Kế tiếp",
  switchToNext: "Chuyển sang hội thoại kế tiếp",
  previousConversation: "Quay về",
  switchToPrevious: "Chuyển về hội thoại trước đó",
  nextUserMessage: "Tin nhắn kế tiếp",
  jumpToNextMessage: "Nhảy đến tin nhắn người dùng kế tiếp",
  previousUserMessage: "Tin nhắn trước",
  jumpToPreviousMessage: "Nhảy đến tin nhắn người dùng trước đó",
  viewDiffs: "Xem các thay đổi",
  openGitDiffViewer: "Mở trình duyệt thay đổi Git",
  addRemoveModelsKeys: "Thêm/xóa models và keys",
  configureModels: "Quản lý models và API keys",
  notificationSettings: "Cài đặt thông báo",
  configureNotifications: "Quản lý cài đặt thông báo",
  enableMarkdownAgent: "Kích hoạt markdown cho agent",
  renderMarkdownAgent: "Render markdown chỉ cho tin nhắn gửi từ agent",
  enableMarkdownAll: "Kích hoạt markdown toàn bộ",
  renderMarkdownAll: "Render markdown cho tất cả tin nhắn",
  disableMarkdown: "Tắt Markdown",
  showPlainText: "Chỉ văn bản thuần, không hơn không kém",
  archiveConversationAction: "Lưu trữ",
  archiveCurrentConversation: "Lưu trữ cuộc hội thoại hiện tại",
  newConversationInMainRepo: "Hội thoại mới ở repo chính",
  newConversationInNewWorktree: "Hội thoại mới ở worktree mới",
  createNewWorktree: "Tạo Git worktree mới cho hội thoại",

  // Conversation Drawer
  archived: "Danh sách lưu trữ",
  noArchivedConversations: "Chưa có hội thoại được lưu trữ",
  noConversationsYet: "Chưa có hội thoại",
  startNewToGetStarted: "Thử gửi tin nhắn để bắt đầu",
  backToConversations: "Về danh sách chính",
  viewArchived: "Xem danh sách lưu trữ",
  rename: "Đổi tên",
  archive: "Lưu trữ",
  restore: "Khôi phục",
  deletePermanently: "Xóa vĩnh viễn",
  confirmDelete: "Thật sự chắc chưa?",
  duplicateName: "Bị trùng tên với hội thoại đã có",
  agentIsWorking: "Agent đang làm việc...",
  subagentIsWorking: "Subagent đang làm việc...",
  hideSubagents: "Ẩn subagent",
  showSubagents: "Hiện subagent",
  groupConversations: "Gộp nhóm các hội thoại",
  noGrouping: "Tắt gộp nhóm",
  directory: "Thư mục",
  gitRepo: "Git Repo",
  other: "Khác",
  collapseSubagents: "Thu gọn subagent",
  expandSubagents: "Mở rộng subagent",
  collapseSidebar: "Thu gọn sidebar",
  closeConversations: "Đóng hội thoại",
  yesterday: "Hôm qua",
  daysAgo: "ngày trước",

  // Message Input
  messagePlaceholder: "Gửi tin nhắn, ảnh hoặc file...",
  messagePlaceholderShort: "Tin nhắn...",
  attachFile: "Đính kèm file",
  sendMessage: "Gửi",
  startVoiceInput: "Bắt đầu nhập bằng giọng nói",
  stopVoiceInput: "Dừng",
  dropFilesHere: "Kéo thả file vào đây",
  uploading: "Đang tải lên...",
  uploadFailed: "Tải lên thất bại",

  // Models Modal
  manageModels: "Quản lý models",
  addModel: "Thêm model",
  editModel: "Chỉnh sửa model",
  loadingModels: "Đang tải danh sách models...",
  providerApiFormat: "Provider / API Format",
  endpoint: "Endpoint",
  defaultEndpoint: "Endpoint mặc định",
  customEndpoint: "Endpoint bên thứ 3",
  model: "Model",
  displayName: "Tên hiển thị",
  nameShownInSelector: "Tên ở phần chọn model",
  apiKey: "API Key",
  enterApiKey: "Nhập API key",
  maxContextTokens: "Cửa sổ ngữ cảnh tối đa",
  tags: "Tags",
  tagsPlaceholder: "phân cách bằng dấu phẩy, ví dụ slug, rẻ",
  tagsTooltip:
    'Sử dụng tag phân cách bằng dấu phẩy cho model. Dùng "slug" để đánh dấu model này làm model tạo tiêu đề hội thoại. Nếu không có model nào có tag "slug", model của hội thoại sẽ được sử dụng.',
  reasoningEffort: "Mức độ suy nghĩ",
  reasoningEffortPlaceholder: "ví dụ: medium, high, xhigh, none — để trống là mức mặc định",
  reasoningEffortHint:
    "Được gửi như reasoning.effort đến OpenAI Responses API. Các nhà cung cấp sẽ bổ sung tier mới định kỳ",
  testButton: "Test",
  testingButton: "Đang test...",
  save: "Lưu",
  cancel: "Hủy",
  duplicate: "Copy",
  delete_: "Xóa",
  modelNameRequired: "Cần có tên model",
  apiKeyRequired: "Cần có API key",
  noModelsConfigured: "Chưa có model nào",
  noModelsHint:
    "Đặt biến môi trường ANTHROPIC_API_KEY, hoặc dùng flag -gateway, hoặc thêm model bên dưới.",

  // Notifications Modal
  notifications: "Thông báo",
  browserNotifications: "Thông báo trình duyệt",
  faviconBadge: "Status trên favicon",
  editChannel: "Chỉnh kênh",
  addChannel: "Thêm kênh",
  customChannels: "Kênh tùy chỉnh",
  noCustomChannels: "Không có kênh tùy chỉnh được thiết lập",
  addWebhookHint: "Thêm webhook URL mới để nhận thông báo",
  channelName: "Tên kênh",
  channelType: "Loại",
  webhookUrl: "Webhook URL",
  enabled: "Kích hoạt",
  testNotification: "Test",
  denied: "Từ chối",
  noServerChannelsConfigured: "Không có kênh server nào được thiết lập",
  addOne: "Thêm",
  edit: "Tùy chỉnh",

  // Diff Viewer
  noFiles: "Không có file",
  chooseFile: "Chọn file để xem diffs",
  commentMode: "Chế độ bình luận",
  editMode: "Chế độ chỉnh sửa",

  // Directory Picker
  newFolderName: "Thư mục mới",
  create: "Tạo",
  noMatchingDirectories: "Không có thư mục khớp",
  noSubdirectories: "Không có thư mục con",
  createNewFolder: "Tạo thư mục mới",

  // Messages
  copyCommitHash: "Copy commit hash",
  clickToCopyCommitHash: "Click để copy commit hash",
  unknownTool: "Tool không xác định",
  toolOutput: "Đầu ra tool",
  errorOccurred: "Có lỗi đã xảy ra",

  // Version
  updateAvailable: "Hiện có phiên bản mới",

  // Welcome / Empty State
  welcomeTitle: "Shelley Agent",
  welcomeSubtitle: "",
  welcomeMessage:
    "Shelley là một agent lập trình chạy trên {hostname}. Bạn có thể yêu cầu Shelley xây dựng dự án. Nếu bạn build website bằng Shelley, bạn có thể dùng HTTP proxy của exe.dev ({docsLink}) để xem tại {proxyLink}.",
  sendMessageToStart: "Gửi tin nhắn để bắt đầu trò chuyện.",
  noModelsConfiguredHint: "Không có model AI sẵn sàng. Nhấn Ctrl+K hoặc ⌘+K để thêm model.",

  // Status Bar
  modelLabel: "Model:",
  dirLabel: "Thư mục:",

  // Sidebar buttons
  editUserAgentsMd: "Chỉnh sửa AGENTS.md",

  openConversations: "Mở lịch sử trò chuyện",
  expandSidebar: "Mở rộng sidebar",

  // Language
  language: "Ngôn ngữ",
  switchLanguage: "Chỉnh ngôn ngữ",
  reportBug: "Báo lỗi",
  english: "English",
  japanese: "日本語",
  french: "Français",
  russian: "Русский",
  spanish: "Español",
  upgoerFive: "Up-Goer Five",
  simplifiedChinese: "简体中文",
  traditionalChinese: "繁體中文",
  vietnamese: "Tiếng Việt",
};
