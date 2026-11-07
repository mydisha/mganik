import "@fontsource/ibm-plex-sans/400.css";
import "@fontsource/ibm-plex-sans/500.css";
import "@fontsource/ibm-plex-sans/600.css";
import "@fontsource/ibm-plex-mono/400.css";
import "@fontsource/ibm-plex-mono/500.css";
import "./app/styles.css";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./app/app";

// Tema mengikuti sistem; dipasang sebelum render pertama supaya tidak berkedip.
const prefersDark = window.matchMedia?.("(prefers-color-scheme: dark)").matches ?? false;
document.documentElement.classList.toggle("dark", prefersDark);

const root = document.getElementById("root");
if (!root) throw new Error("elemen #root tidak ditemukan");

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
