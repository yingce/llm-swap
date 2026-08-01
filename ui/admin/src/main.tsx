import React from "react";
import { createRoot } from "react-dom/client";
import { App } from "./app/App";
import "./styles.css";

createRoot(document.getElementById("llmswap-admin-root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);
