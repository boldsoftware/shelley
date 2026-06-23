import React from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import ExportPage, { exportConversationIdFromPath } from "./components/ExportPage";
import { initializeTheme } from "./services/theme";
import { initializeNotifications } from "./services/notifications";
import { MarkdownProvider } from "./contexts/MarkdownContext";
import { I18nProvider } from "./i18n";
import { TooltipProvider } from "./components/ui/tooltip";
import { Toaster } from "./components/ui/sonner";

// Apply theme before render to avoid flash
initializeTheme();

const rootContainer = document.getElementById("root");
if (!rootContainer) throw new Error("Root container not found");

const root = createRoot(rootContainer);

// The standalone conversation export view is served at /export/<id>. It's a
// focused, read-mostly page (fetch + render markdown), so we mount it directly
// instead of the full chat App and skip app-wide side effects like the
// notification/favicon system.
const exportId = exportConversationIdFromPath();
if (exportId) {
  root.render(
    <MarkdownProvider>
      <ExportPage conversationId={exportId} />
    </MarkdownProvider>,
  );
} else {
  // Initialize notification system (includes favicon)
  initializeNotifications();

  root.render(
    <I18nProvider>
      <MarkdownProvider>
        <TooltipProvider delayDuration={300}>
          <App />
          <Toaster />
        </TooltipProvider>
      </MarkdownProvider>
    </I18nProvider>,
  );
}
