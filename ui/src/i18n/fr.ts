import type { TranslationKeys } from "./types";

export const fr: TranslationKeys = {
  // App-level
  loading: "Chargement...",
  retry: "Réessayer",
  failedToLoadConversations:
    "Impossible de charger les conversations. Veuillez rafraîchir la page.",

  // Chat Header & Actions
  newConversation: "Nouvelle conversation",
  moreOptions: "Plus d'options",
  conversations: "Conversations",

  // Overflow Menu
  diffs: "Différences",
  terminal: "Terminal",
  archiveConversation: "Archiver la conversation",
  checkForNewVersion: "Vérifier les mises à jour",
  markdown: "Markdown",
  off: "Désactivé",
  agent: "Agent",
  all: "Tout",

  // Theme
  system: "Système",
  light: "Clair",
  dark: "Sombre",

  // Notifications
  enableNotifications: "Activer les notifications",
  disableNotifications: "Désactiver les notifications",
  blockedByBrowser: "Bloqué par le navigateur",
  osNotificationsWhenHidden: "Notifications système lorsque l'onglet est masqué",
  requiresBrowserPermission: "Nécessite l'autorisation du navigateur",
  on: "Activé",

  // Command Palette
  searchPlaceholder: "Rechercher des conversations ou des actions...",
  searching: "Recherche en cours...",
  noResults: "Aucun résultat trouvé",
  toNavigate: "pour naviguer",
  toSelect: "pour sélectionner",
  toClose: "pour fermer",
  action: "Action",

  // Command Palette Actions
  newConversationAction: "Nouvelle conversation",
  startNewConversation: "Démarrer une nouvelle conversation",
  nextConversation: "Conversation suivante",
  switchToNext: "Passer à la conversation suivante",
  previousConversation: "Conversation précédente",
  switchToPrevious: "Passer à la conversation précédente",
  nextUserMessage: "Message utilisateur suivant",
  jumpToNextMessage: "Aller au message utilisateur suivant",
  previousUserMessage: "Message utilisateur précédent",
  jumpToPreviousMessage: "Aller au message utilisateur précédent",
  viewDiffs: "Voir les différences",
  openGitDiffViewer: "Ouvrir le visualiseur de différences Git",
  addRemoveModelsKeys: "Ajouter/Supprimer des modèles et des clés",
  configureModels: "Configurer les modèles d'IA et les clés API",
  notificationSettings: "Paramètres de notification",
  configureNotifications: "Configurer les préférences de notification",
  enableMarkdownAgent: "Activer le Markdown (Agent)",
  renderMarkdownAgent: "Afficher le Markdown pour les messages de l'agent",
  enableMarkdownAll: "Activer le Markdown (Tout)",
  renderMarkdownAll: "Afficher le Markdown pour tous les messages",
  disableMarkdown: "Désactiver le Markdown",
  showPlainText: "Afficher le texte brut pour tous les messages",
  archiveConversationAction: "Archiver la conversation",
  archiveCurrentConversation: "Archiver la conversation en cours",
  newConversationInMainRepo: "Nouvelle conversation dans le dépôt principal",
  newConversationInNewWorktree: "Nouvelle conversation dans un nouveau Worktree",
  createNewWorktree: "Créer un nouveau worktree Git pour cette conversation",

  // Conversation Drawer
  archived: "Archivées",
  noArchivedConversations: "Aucune conversation archivée",
  noConversationsYet: "Aucune conversation pour le moment",
  startNewToGetStarted: "Démarrez une nouvelle conversation pour commencer",
  backToConversations: "Retour aux conversations",
  viewArchived: "Voir les archives",
  rename: "Renommer",
  archive: "Archiver",
  restore: "Restaurer",
  deletePermanently: "Supprimer définitivement",
  confirmDelete: "Êtes-vous sûr ? Cette action est irréversible.",
  duplicateName: "Une conversation portant ce nom existe déjà",
  agentIsWorking: "L'agent travaille...",
  subagentIsWorking: "Le sous-agent travaille...",
  hideSubagents: "Masquer les sous-agents",
  showSubagents: "Afficher les sous-agents",
  groupConversations: "Grouper les conversations",
  noGrouping: "Aucun regroupement",
  directory: "Répertoire",
  gitRepo: "Dépôt Git",
  other: "Autre",
  collapseSubagents: "Réduire les sous-agents",
  expandSubagents: "Développer les sous-agents",
  collapseSidebar: "Réduire la barre latérale",
  closeConversations: "Fermer les conversations",
  yesterday: "Hier",
  daysAgo: "jours",

  // Message Input
  messagePlaceholder: "Message, coller une image ou joindre un fichier...",
  messagePlaceholderShort: "Message...",
  attachFile: "Joindre un fichier",
  sendMessage: "Envoyer le message",
  startVoiceInput: "Démarrer la saisie vocale",
  stopVoiceInput: "Arrêter la saisie vocale",
  dropFilesHere: "Déposez les fichiers ici",
  uploading: "Téléversement...",
  uploadFailed: "Échec du téléversement",

  // Models Modal
  manageModels: "Gérer les modèles",
  addModel: "Ajouter un modèle",
  editModel: "Modifier le modèle",
  loadingModels: "Chargement des modèles...",
  providerApiFormat: "Fournisseur / Format API",
  endpoint: "Point d'accès",
  defaultEndpoint: "Point d'accès par défaut",
  customEndpoint: "Point d'accès personnalisé",
  model: "Modèle",
  displayName: "Nom d'affichage",
  nameShownInSelector: "Nom affiché dans le sélecteur de modèle",
  apiKey: "Clé API",
  enterApiKey: "Saisir la clé API",
  maxContextTokens: "Nombre maximum de tokens de contexte",
  tags: "Étiquettes",
  tagsPlaceholder: "Ajouter des étiquettes...",
  tagsTooltip: "Les étiquettes aident à organiser et filtrer les modèles",
  testButton: "Tester",
  testingButton: "Test en cours...",
  save: "Enregistrer",
  cancel: "Annuler",
  duplicate: "Dupliquer",
  delete_: "Supprimer",
  modelNameRequired: "Le nom du modèle est requis",
  apiKeyRequired: "La clé API est requise",
  noModelsConfigured: "Aucun modèle configuré",
  noModelsHint: "Ajoutez un modèle pour commencer",

  // Notifications Modal
  notifications: "Notifications",
  browserNotifications: "Notifications du navigateur",
  faviconBadge: "Badge de favicon",
  editChannel: "Modifier le canal",
  addChannel: "Ajouter un canal",
  customChannels: "Canaux personnalisés",
  noCustomChannels: "Aucun canal personnalisé configuré",
  addWebhookHint: "Ajoutez une URL de Webhook pour recevoir des notifications",
  channelName: "Nom du canal",
  channelType: "Type de canal",
  webhookUrl: "URL du Webhook",
  enabled: "Activé",
  testNotification: "Notification de test",
  denied: "Refusé",
  noServerChannelsConfigured: "Aucun canal serveur configuré",
  addOne: "En ajouter un",
  edit: "Modifier",

  // Diff Viewer
  noFiles: "Aucun fichier",
  chooseFile: "Choisissez un fichier pour voir les différences",
  commentMode: "Mode commentaire",
  editMode: "Mode édition",

  // Directory Picker
  newFolderName: "Nom du nouveau dossier",
  create: "Créer",
  noMatchingDirectories: "Aucun répertoire correspondant",
  noSubdirectories: "Aucun sous-répertoire",
  createNewFolder: "Créer un nouveau dossier",

  // Messages
  copyCommitHash: "Copier le hash du commit",
  clickToCopyCommitHash: "Cliquez pour copier le hash du commit",
  unknownTool: "Outil inconnu",
  toolOutput: "Sortie de l'outil",
  errorOccurred: "Une erreur est survenue",

  // Version
  updateAvailable: "Mise à jour disponible",

  // Welcome / Empty State
  welcomeTitle: "Shelley Agent",
  welcomeSubtitle: "",
  welcomeMessage:
    "Shelley est un agent qui s'exécute sur {hostname}. Vous pouvez demander à Shelley de faire des choses. Si vous créez un site web avec Shelley, vous pouvez utiliser les fonctionnalités de proxy d'exe.dev (voir {docsLink}) pour y accéder via {proxyLink}.",
  sendMessageToStart: "Envoyez un message pour démarrer la conversation.",
  noModelsConfiguredHint:
    "Aucun modèle IA configuré. Appuyez sur Ctrl+K ou ⌘+K pour ajouter un modèle.",

  // Status Bar
  modelLabel: "Modèle :",
  dirLabel: "Rép. :",

  // Sidebar buttons
  openConversations: "Ouvrir les conversations",
  expandSidebar: "Développer la barre latérale",

  // Language
  language: "Langue",
  switchLanguage: "Changer de langue",
  english: "English",
  japanese: "日本語",
  french: "Français",
  russian: "Русский",
  spanish: "Español",
};
