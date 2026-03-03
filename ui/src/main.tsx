import React from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { initializeTheme } from "./services/theme";
import { initializeNotifications } from "./services/notifications";
import { MarkdownProvider } from "./contexts/MarkdownContext";
import { I18nProvider } from "./i18n";

// Apply theme before render to avoid flash
initializeTheme();

// Initialize notification system (includes favicon)
initializeNotifications();

// Render main app
const rootContainer = document.getElementById("root");
if (!rootContainer) throw new Error("Root container not found");

const root = createRoot(rootContainer);
root.render(
  <I18nProvider>
    <MarkdownProvider>
      <App />
    </MarkdownProvider>
  </I18nProvider>,
);
