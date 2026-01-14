import React from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { initializeTheme } from "./services/theme";
import { initializeFavicon } from "./services/favicon";

// Apply theme before render to avoid flash
initializeTheme();

// Initialize dynamic favicon
initializeFavicon();

// Render main app
const rootContainer = document.getElementById("root");
if (!rootContainer) throw new Error("Root container not found");

const root = createRoot(rootContainer);
root.render(<App />);
