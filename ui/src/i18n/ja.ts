import type { TranslationKeys } from "./types";

export const ja: TranslationKeys = {
  // App-level
  loading: "読み込み中...",
  retry: "再試行",
  failedToLoadConversations: "会話の読み込みに失敗しました。ページを更新してください。",

  // Chat Header & Actions
  newConversation: "新しい会話",
  moreOptions: "その他のオプション",
  conversations: "会話",

  // Overflow Menu
  diffs: "差分",
  terminal: "ターミナル",
  archiveConversation: "会話をアーカイブ",
  checkForNewVersion: "新しいバージョンを確認",
  markdown: "Markdown",
  off: "オフ",
  agent: "エージェント",
  all: "すべて",

  // Theme
  system: "システム",
  light: "ライト",
  dark: "ダーク",

  // Notifications
  enableNotifications: "通知を有効にする",
  disableNotifications: "通知を無効にする",
  blockedByBrowser: "ブラウザによりブロック中",
  osNotificationsWhenHidden: "タブが非表示の時にOS通知を表示",
  requiresBrowserPermission: "ブラウザの許可が必要です",
  on: "オン",

  // Command Palette
  searchPlaceholder: "会話またはアクションを検索...",
  searching: "検索中...",
  noResults: "結果が見つかりません",
  toNavigate: "移動",
  toSelect: "選択",
  toClose: "閉じる",
  action: "アクション",

  // Command Palette Actions
  newConversationAction: "新しい会話",
  startNewConversation: "新しい会話を始める",
  nextConversation: "次の会話",
  switchToNext: "次の会話に切り替える",
  previousConversation: "前の会話",
  switchToPrevious: "前の会話に切り替える",
  nextUserMessage: "次のユーザーメッセージ",
  jumpToNextMessage: "次のユーザーメッセージに移動",
  previousUserMessage: "前のユーザーメッセージ",
  jumpToPreviousMessage: "前のユーザーメッセージに移動",
  viewDiffs: "差分を表示",
  openGitDiffViewer: "Git差分ビューアを開く",
  addRemoveModelsKeys: "モデルとキーの追加・削除",
  configureModels: "AIモデルとAPIキーを設定",
  notificationSettings: "通知設定",
  configureNotifications: "通知の設定を変更",
  enableMarkdownAgent: "Markdownを有効にする（エージェント）",
  renderMarkdownAgent: "エージェントメッセージをMarkdownで表示",
  enableMarkdownAll: "Markdownを有効にする（すべて）",
  renderMarkdownAll: "すべてのメッセージをMarkdownで表示",
  disableMarkdown: "Markdownを無効にする",
  showPlainText: "すべてのメッセージをプレーンテキストで表示",
  archiveConversationAction: "会話をアーカイブ",
  archiveCurrentConversation: "現在の会話をアーカイブする",
  newConversationInMainRepo: "メインリポジトリで新しい会話",
  newConversationInNewWorktree: "新しいWorktreeで新しい会話",
  createNewWorktree: "この会話用に新しいGit Worktreeを作成する",

  // Conversation Drawer
  archived: "アーカイブ済み",
  noArchivedConversations: "アーカイブされた会話はありません",
  noConversationsYet: "会話はまだありません",
  startNewToGetStarted: "新しい会話を開始してください",
  backToConversations: "会話一覧に戻る",
  viewArchived: "アーカイブを表示",
  rename: "名前を変更",
  archive: "アーカイブ",
  restore: "復元",
  deletePermanently: "完全に削除",
  confirmDelete: "本当に削除しますか？この操作は元に戻せません。",
  duplicateName: "同じ名前の会話がすでに存在します",
  agentIsWorking: "エージェントが作業中...",
  subagentIsWorking: "サブエージェントが作業中...",
  hideSubagents: "サブエージェントを非表示",
  showSubagents: "サブエージェントを表示",
  groupConversations: "会話をグループ化",
  noGrouping: "グループなし",
  directory: "ディレクトリ",
  gitRepo: "Gitリポジトリ",
  other: "その他",
  collapseSubagents: "サブエージェントを折りたたむ",
  expandSubagents: "サブエージェントを展開",
  collapseSidebar: "サイドバーを折りたたむ",
  closeConversations: "会話を閉じる",
  yesterday: "昨日",
  daysAgo: "日前",

  // Message Input
  messagePlaceholder: "メッセージ、画像の貼り付け、またはファイルを添付...",
  messagePlaceholderShort: "メッセージ...",
  attachFile: "ファイルを添付",
  sendMessage: "メッセージを送信",
  startVoiceInput: "音声入力を開始",
  stopVoiceInput: "音声入力を停止",
  dropFilesHere: "ここにファイルをドロップ",
  uploading: "アップロード中...",
  uploadFailed: "アップロードに失敗しました",

  // Models Modal
  manageModels: "モデルの管理",
  addModel: "モデルの追加",
  editModel: "モデルの編集",
  loadingModels: "モデルを読み込み中...",
  providerApiFormat: "プロバイダー / API形式",
  endpoint: "エンドポイント",
  defaultEndpoint: "デフォルトのエンドポイント",
  customEndpoint: "カスタムエンドポイント",
  model: "モデル",
  displayName: "表示名",
  nameShownInSelector: "モデル選択に表示される名前",
  apiKey: "APIキー",
  enterApiKey: "APIキーを入力",
  maxContextTokens: "最大コンテキストトークン数",
  tags: "タグ",
  tagsPlaceholder: "カンマ区切り、例: slug, cheap",
  tagsTooltip:
    'このモデル用のカンマ区切りのタグ。会話タイトル生成用のモデルとしてマークするには"slug"を使用します。"slug"タグを持つモデルがない場合は、会話のモデルが使用されます。',
  testButton: "テスト",
  testingButton: "テスト中...",
  save: "保存",
  cancel: "キャンセル",
  duplicate: "複製",
  delete_: "削除",
  modelNameRequired: "モデル名は必須です",
  apiKeyRequired: "APIキーは必須です",
  noModelsConfigured: "モデルが設定されていません",
  noModelsHint:
    "ANTHROPIC_API_KEY等の環境変数を設定するか、-gatewayフラグを使用するか、以下でカスタムモデルを追加してください。",

  // Notifications Modal
  notifications: "通知",
  browserNotifications: "ブラウザ通知",
  faviconBadge: "ファビコンバッジ",
  editChannel: "チャンネルを編集",
  addChannel: "チャンネルを追加",
  customChannels: "カスタムチャンネル",
  noCustomChannels: "カスタムチャンネルは設定されていません",
  addWebhookHint: "通知を受け取るためのWebhook URLを追加してください",
  channelName: "チャンネル名",
  channelType: "チャンネルタイプ",
  webhookUrl: "Webhook URL",
  enabled: "有効",
  testNotification: "テスト通知",
  denied: "拒否されました",
  noServerChannelsConfigured: "サーバーチャンネルが設定されていません",
  addOne: "追加する",
  edit: "編集",

  // Diff Viewer
  noFiles: "ファイルなし",
  chooseFile: "差分を表示するファイルを選択してください",
  commentMode: "コメントモード",
  editMode: "編集モード",

  // Directory Picker
  newFolderName: "新しいフォルダ名",
  create: "作成",
  noMatchingDirectories: "一致するディレクトリがありません",
  noSubdirectories: "サブディレクトリがありません",
  createNewFolder: "新しいフォルダを作成",

  // Messages
  copyCommitHash: "コミットハッシュをコピー",
  clickToCopyCommitHash: "クリックしてコミットハッシュをコピー",
  unknownTool: "不明なツール",
  toolOutput: "ツール出力",
  errorOccurred: "エラーが発生しました",

  // Version
  updateAvailable: "アップデートがあります",

  // Welcome / Empty State
  welcomeTitle: "Shelley Agent",
  welcomeSubtitle: "",
  welcomeMessage:
    "Shelleyは{hostname}で動作するエージェントです。Shelleyに作業を依頼できます。Shelleyでウェブサイトを構築した場合、exe.devのプロキシ機能（{docsLink}を参照）を使用して{proxyLink}からアクセスできます。",
  sendMessageToStart: "メッセージを送信して会話を開始してください。",
  noModelsConfiguredHint:
    "AIモデルが設定されていません。Ctrl+K または ⌘+K でモデルを追加してください。",

  // Status Bar
  modelLabel: "モデル:",
  dirLabel: "ディレクトリ:",

  // Sidebar buttons
  openConversations: "会話を開く",
  expandSidebar: "サイドバーを展開",

  // Language
  language: "言語",
  switchLanguage: "言語を切り替える",
  reportBug: "バグを報告",
  english: "English",
  japanese: "日本語",
  french: "Français",
  russian: "Русский",
  spanish: "Español",
  upgoerFive: "Up-Goer Five",
  simplifiedChinese: "简体中文",
  traditionalChinese: "繁體中文",
};
