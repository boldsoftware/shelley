import type { TranslationKeys } from "./types";

export const ru: TranslationKeys = {
  // App-level
  loading: "Загрузка...",
  retry: "Повторить",
  failedToLoadConversations: "Не удалось загрузить диалоги. Пожалуйста, обновите страницу.",

  // Chat Header & Actions
  newConversation: "Новый диалог",
  moreOptions: "Ещё",
  conversations: "Диалоги",

  // Overflow Menu
  diffs: "Изменения",
  terminal: "Терминал",
  archiveConversation: "Архивировать диалог",
  checkForNewVersion: "Проверить обновления",
  markdown: "Markdown",
  off: "Откл",
  agent: "Агент",
  all: "Все",

  // Theme
  system: "Системная",
  light: "Светлая",
  dark: "Тёмная",

  // Notifications
  enableNotifications: "Включить уведомления",
  disableNotifications: "Отключить уведомления",
  blockedByBrowser: "Заблокировано браузером",
  osNotificationsWhenHidden: "Уведомления ОС при скрытой вкладке",
  requiresBrowserPermission: "Требуется разрешение браузера",
  on: "Вкл",

  // Command Palette
  searchPlaceholder: "Поиск диалогов или действий...",
  searching: "Поиск...",
  noResults: "Ничего не найдено",
  toNavigate: "навигация",
  toSelect: "выбрать",
  toClose: "закрыть",
  action: "Действие",

  // Command Palette Actions
  newConversationAction: "Новый диалог",
  startNewConversation: "Начать новый диалог",
  nextConversation: "Следующий диалог",
  switchToNext: "Перейти к следующему диалогу",
  previousConversation: "Предыдущий диалог",
  switchToPrevious: "Перейти к предыдущему диалогу",
  nextUserMessage: "Следующее сообщение пользователя",
  jumpToNextMessage: "Перейти к следующему сообщению пользователя",
  previousUserMessage: "Предыдущее сообщение пользователя",
  jumpToPreviousMessage: "Перейти к предыдущему сообщению пользователя",
  viewDiffs: "Просмотр изменений",
  openGitDiffViewer: "Открыть просмотр изменений Git",
  addRemoveModelsKeys: "Добавить/Удалить модели и ключи",
  configureModels: "Настроить модели ИИ и API-ключи",
  notificationSettings: "Настройки уведомлений",
  configureNotifications: "Настроить параметры уведомлений",
  enableMarkdownAgent: "Включить Markdown (Агент)",
  renderMarkdownAgent: "Отображать Markdown для сообщений агента",
  enableMarkdownAll: "Включить Markdown (Все)",
  renderMarkdownAll: "Отображать Markdown для всех сообщений",
  disableMarkdown: "Отключить Markdown",
  showPlainText: "Показывать простой текст для всех сообщений",
  archiveConversationAction: "Архивировать диалог",
  archiveCurrentConversation: "Архивировать текущий диалог",
  newConversationInMainRepo: "Новый диалог в основном репозитории",
  newConversationInNewWorktree: "Новый диалог в новом worktree",
  createNewWorktree: "Создать новый Git worktree для этого диалога",

  // Conversation Drawer
  archived: "Архив",
  noArchivedConversations: "Нет архивных диалогов",
  noConversationsYet: "Диалогов пока нет",
  startNewToGetStarted: "Начните новый диалог, чтобы приступить к работе",
  backToConversations: "Назад к диалогам",
  viewArchived: "Показать архив",
  rename: "Переименовать",
  archive: "Архивировать",
  restore: "Восстановить",
  deletePermanently: "Удалить навсегда",
  confirmDelete: "Вы уверены? Это действие нельзя отменить.",
  duplicateName: "Диалог с таким именем уже существует",
  agentIsWorking: "Агент работает...",
  subagentIsWorking: "Субагент работает...",
  hideSubagents: "Скрыть субагентов",
  showSubagents: "Показать субагентов",
  groupConversations: "Группировать диалоги",
  noGrouping: "Без группировки",
  directory: "Каталог",
  gitRepo: "Git-репозиторий",
  other: "Другое",
  collapseSubagents: "Свернуть субагентов",
  expandSubagents: "Развернуть субагентов",
  collapseSidebar: "Свернуть боковую панель",
  closeConversations: "Закрыть диалоги",
  yesterday: "Вчера",
  daysAgo: "дн. назад",

  // Message Input
  messagePlaceholder: "Сообщение, вставьте изображение или прикрепите файл...",
  messagePlaceholderShort: "Сообщение...",
  attachFile: "Прикрепить файл",
  sendMessage: "Отправить сообщение",
  startVoiceInput: "Начать голосовой ввод",
  stopVoiceInput: "Остановить голосовой ввод",
  dropFilesHere: "Перетащите файлы сюда",
  uploading: "Загрузка...",
  uploadFailed: "Ошибка загрузки",

  // Models Modal
  manageModels: "Управление моделями",
  addModel: "Добавить модель",
  editModel: "Редактировать модель",
  loadingModels: "Загрузка моделей...",
  providerApiFormat: "Провайдер / Формат API",
  endpoint: "Эндпоинт",
  defaultEndpoint: "Эндпоинт по умолчанию",
  customEndpoint: "Пользовательский эндпоинт",
  model: "Модель",
  displayName: "Отображаемое имя",
  nameShownInSelector: "Имя, отображаемое в селекторе моделей",
  apiKey: "API-ключ",
  enterApiKey: "Введите API-ключ",
  maxContextTokens: "Макс. токенов контекста",
  tags: "Теги",
  tagsPlaceholder: "через запятую, напр., slug, cheap",
  tagsTooltip:
    'Теги через запятую для этой модели. Используйте "slug", чтобы отметить модель для генерации заголовков диалогов. Если ни одна модель не имеет тега "slug", будет использована модель диалога.',
  testButton: "Тест",
  testingButton: "Тестирование...",
  save: "Сохранить",
  cancel: "Отмена",
  duplicate: "Дублировать",
  delete_: "Удалить",
  modelNameRequired: "Название модели обязательно",
  apiKeyRequired: "API-ключ обязателен",
  noModelsConfigured: "Модели не настроены",
  noModelsHint:
    "Установите переменные окружения, например ANTHROPIC_API_KEY, используйте флаг -gateway или добавьте пользовательскую модель ниже.",

  // Notifications Modal
  notifications: "Уведомления",
  browserNotifications: "Уведомления браузера",
  faviconBadge: "Значок на иконке",
  editChannel: "Редактировать канал",
  addChannel: "Добавить канал",
  customChannels: "Пользовательские каналы",
  noCustomChannels: "Пользовательские каналы не настроены",
  addWebhookHint: "Добавьте URL вебхука для получения уведомлений",
  channelName: "Название канала",
  channelType: "Тип канала",
  webhookUrl: "URL вебхука",
  enabled: "Включено",
  testNotification: "Тестовое уведомление",
  denied: "Запрещено",
  noServerChannelsConfigured: "Серверные каналы не настроены",
  addOne: "Добавить",
  edit: "Редактировать",

  // Diff Viewer
  noFiles: "Нет файлов",
  chooseFile: "Выберите файл для просмотра изменений",
  commentMode: "Режим комментариев",
  editMode: "Режим редактирования",

  // Directory Picker
  newFolderName: "Имя новой папки",
  create: "Создать",
  noMatchingDirectories: "Нет подходящих каталогов",
  noSubdirectories: "Нет подкаталогов",
  createNewFolder: "Создать новую папку",

  // Messages
  copyCommitHash: "Копировать хеш коммита",
  clickToCopyCommitHash: "Нажмите, чтобы скопировать хеш коммита",
  unknownTool: "Неизвестный инструмент",
  toolOutput: "Вывод инструмента",
  errorOccurred: "Произошла ошибка",

  // Version
  updateAvailable: "Доступно обновление",

  // Welcome / Empty State
  welcomeTitle: "Shelley Agent",
  welcomeSubtitle: "",
  welcomeMessage:
    "Shelley — это агент, работающий на {hostname}. Вы можете попросить Shelley выполнить задачи. Если вы создали веб-сайт с помощью Shelley, вы можете использовать прокси-функции exe.dev (см. {docsLink}), чтобы открыть его по адресу {proxyLink}.",
  sendMessageToStart: "Отправьте сообщение, чтобы начать диалог.",
  noModelsConfiguredHint: "Модели ИИ не настроены. Нажмите Ctrl+K или ⌘+K, чтобы добавить модель.",

  // Status Bar
  modelLabel: "Модель:",
  dirLabel: "Каталог:",

  // Sidebar buttons
  openConversations: "Открыть диалоги",
  expandSidebar: "Развернуть боковую панель",

  // Language
  language: "Язык",
  switchLanguage: "Сменить язык",
  reportBug: "Сообщить об ошибке",
  english: "English",
  japanese: "日本語",
  french: "Français",
  russian: "Русский",
  spanish: "Español",
  upgoerFive: "Up-Goer Five",
  simplifiedChinese: "简体中文",
  traditionalChinese: "繁體中文",
};
