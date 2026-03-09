import type { TranslationKeys } from "./types";

export const es: TranslationKeys = {
  // App-level
  loading: "Cargando...",
  retry: "Reintentar",
  failedToLoadConversations: "No se pudieron cargar las conversaciones. Actualice la página.",

  // Chat Header & Actions
  newConversation: "Nueva conversación",
  moreOptions: "Más opciones",
  conversations: "Conversaciones",

  // Overflow Menu
  diffs: "Diferencias",
  terminal: "Terminal",
  archiveConversation: "Archivar conversación",
  checkForNewVersion: "Buscar nueva versión",
  markdown: "Markdown",
  off: "Desactivado",
  agent: "Agente",
  all: "Todo",

  // Theme
  system: "Sistema",
  light: "Claro",
  dark: "Oscuro",

  // Notifications
  enableNotifications: "Activar notificaciones",
  disableNotifications: "Desactivar notificaciones",
  blockedByBrowser: "Bloqueado por el navegador",
  osNotificationsWhenHidden: "Notificaciones del sistema cuando la pestaña está oculta",
  requiresBrowserPermission: "Requiere permiso del navegador",
  on: "Activado",

  // Command Palette
  searchPlaceholder: "Buscar conversaciones o acciones...",
  searching: "Buscando...",
  noResults: "No se encontraron resultados",
  toNavigate: "para navegar",
  toSelect: "para seleccionar",
  toClose: "para cerrar",
  action: "Acción",

  // Command Palette Actions
  newConversationAction: "Nueva conversación",
  startNewConversation: "Iniciar una nueva conversación",
  nextConversation: "Siguiente conversación",
  switchToNext: "Cambiar a la siguiente conversación",
  previousConversation: "Conversación anterior",
  switchToPrevious: "Cambiar a la conversación anterior",
  nextUserMessage: "Siguiente mensaje del usuario",
  jumpToNextMessage: "Ir al siguiente mensaje del usuario",
  previousUserMessage: "Mensaje anterior del usuario",
  jumpToPreviousMessage: "Ir al mensaje anterior del usuario",
  viewDiffs: "Ver diferencias",
  openGitDiffViewer: "Abrir el visor de diferencias de Git",
  addRemoveModelsKeys: "Agregar/Eliminar modelos y claves",
  configureModels: "Configurar modelos de IA y claves de API",
  notificationSettings: "Configuración de notificaciones",
  configureNotifications: "Configurar preferencias de notificaciones",
  enableMarkdownAgent: "Activar Markdown (Agente)",
  renderMarkdownAgent: "Renderizar Markdown en mensajes del agente",
  enableMarkdownAll: "Activar Markdown (Todo)",
  renderMarkdownAll: "Renderizar Markdown en todos los mensajes",
  disableMarkdown: "Desactivar Markdown",
  showPlainText: "Mostrar texto sin formato en todos los mensajes",
  archiveConversationAction: "Archivar conversación",
  archiveCurrentConversation: "Archivar la conversación actual",
  newConversationInMainRepo: "Nueva conversación en el repositorio principal",
  newConversationInNewWorktree: "Nueva conversación en nuevo worktree",
  createNewWorktree: "Crear un nuevo worktree de Git para esta conversación",

  // Conversation Drawer
  archived: "Archivadas",
  noArchivedConversations: "No hay conversaciones archivadas",
  noConversationsYet: "Aún no hay conversaciones",
  startNewToGetStarted: "Inicie una nueva conversación para comenzar",
  backToConversations: "Volver a conversaciones",
  viewArchived: "Ver archivadas",
  rename: "Renombrar",
  archive: "Archivar",
  restore: "Restaurar",
  deletePermanently: "Eliminar permanentemente",
  confirmDelete: "¿Está seguro? Esta acción no se puede deshacer.",
  duplicateName: "Ya existe una conversación con este nombre",
  agentIsWorking: "El agente está trabajando...",
  subagentIsWorking: "El subagente está trabajando...",
  hideSubagents: "Ocultar subagentes",
  showSubagents: "Mostrar subagentes",
  groupConversations: "Agrupar conversaciones",
  noGrouping: "Sin agrupación",
  directory: "Directorio",
  gitRepo: "Repositorio Git",
  other: "Otro",
  collapseSubagents: "Contraer subagentes",
  expandSubagents: "Expandir subagentes",
  collapseSidebar: "Contraer barra lateral",
  closeConversations: "Cerrar conversaciones",
  yesterday: "Ayer",
  daysAgo: "días atrás",

  // Message Input
  messagePlaceholder: "Mensaje, pegue una imagen o adjunte un archivo...",
  messagePlaceholderShort: "Mensaje...",
  attachFile: "Adjuntar archivo",
  sendMessage: "Enviar mensaje",
  startVoiceInput: "Iniciar entrada de voz",
  stopVoiceInput: "Detener entrada de voz",
  dropFilesHere: "Suelte los archivos aquí",
  uploading: "Subiendo...",
  uploadFailed: "Error al subir",

  // Models Modal
  manageModels: "Administrar modelos",
  addModel: "Agregar modelo",
  editModel: "Editar modelo",
  loadingModels: "Cargando modelos...",
  providerApiFormat: "Proveedor / Formato de API",
  endpoint: "Endpoint",
  defaultEndpoint: "Endpoint predeterminado",
  customEndpoint: "Endpoint personalizado",
  model: "Modelo",
  displayName: "Nombre para mostrar",
  nameShownInSelector: "Nombre que se muestra en el selector de modelos",
  apiKey: "Clave de API",
  enterApiKey: "Ingrese la clave de API",
  maxContextTokens: "Tokens de contexto máximos",
  tags: "Etiquetas",
  tagsPlaceholder: "separadas por comas, ej., slug, cheap",
  tagsTooltip:
    'Etiquetas separadas por comas para este modelo. Use "slug" para marcar este modelo para generar títulos de conversación. Si ningún modelo tiene la etiqueta "slug", se usará el modelo de la conversación.',
  testButton: "Probar",
  testingButton: "Probando...",
  save: "Guardar",
  cancel: "Cancelar",
  duplicate: "Duplicar",
  delete_: "Eliminar",
  modelNameRequired: "El nombre del modelo es obligatorio",
  apiKeyRequired: "La clave de API es obligatoria",
  noModelsConfigured: "No hay modelos configurados",
  noModelsHint:
    "Configure variables de entorno como ANTHROPIC_API_KEY, use el flag -gateway, o agregue un modelo personalizado abajo.",

  // Notifications Modal
  notifications: "Notificaciones",
  browserNotifications: "Notificaciones del navegador",
  faviconBadge: "Insignia de favicon",
  editChannel: "Editar canal",
  addChannel: "Agregar canal",
  customChannels: "Canales personalizados",
  noCustomChannels: "No hay canales personalizados configurados",
  addWebhookHint: "Agregue una URL de Webhook para recibir notificaciones",
  channelName: "Nombre del canal",
  channelType: "Tipo de canal",
  webhookUrl: "URL de Webhook",
  enabled: "Activado",
  testNotification: "Notificación de prueba",
  denied: "Denegado",
  noServerChannelsConfigured: "No hay canales de servidor configurados",
  addOne: "Agregar uno",
  edit: "Editar",

  // Diff Viewer
  noFiles: "Sin archivos",
  chooseFile: "Seleccione un archivo para ver las diferencias",
  commentMode: "Modo de comentarios",
  editMode: "Modo de edición",

  // Directory Picker
  newFolderName: "Nombre de la nueva carpeta",
  create: "Crear",
  noMatchingDirectories: "No se encontraron directorios coincidentes",
  noSubdirectories: "Sin subdirectorios",
  createNewFolder: "Crear nueva carpeta",

  // Messages
  copyCommitHash: "Copiar hash del commit",
  clickToCopyCommitHash: "Haga clic para copiar el hash del commit",
  unknownTool: "Herramienta desconocida",
  toolOutput: "Salida de la herramienta",
  errorOccurred: "Ocurrió un error",

  // Version
  updateAvailable: "Actualización disponible",

  // Welcome / Empty State
  welcomeTitle: "Shelley Agent",
  welcomeSubtitle: "",
  welcomeMessage:
    "Shelley es un agente que se ejecuta en {hostname}. Puede pedirle a Shelley que haga cosas. Si crea un sitio web con Shelley, puede usar las funciones de proxy de exe.dev (ver {docsLink}) para visitarlo en {proxyLink}.",
  sendMessageToStart: "Envíe un mensaje para iniciar la conversación.",
  noModelsConfiguredHint:
    "No hay modelos de IA configurados. Presione Ctrl+K o ⌘+K para agregar un modelo.",

  // Status Bar
  modelLabel: "Modelo:",
  dirLabel: "Dir.:",

  // Sidebar buttons
  openConversations: "Abrir conversaciones",
  expandSidebar: "Expandir barra lateral",

  // Language
  language: "Idioma",
  switchLanguage: "Cambiar idioma",
  reportBug: "Reportar un error",
  english: "English",
  japanese: "日本語",
  french: "Français",
  russian: "Русский",
  spanish: "Español",
  upgoerFive: "Up-Goer Five",
  simplifiedChinese: "简体中文",
  traditionalChinese: "繁體中文",
};
