document.addEventListener("DOMContentLoaded", () => {
  let currentWorkspace = "default";
  let tabCounter = 0;
  let activeTabId = null;
  let latestRawResponse = "";

  // Workspace & Environment Variables Elements
  const workspaceSelect = document.getElementById("workspaceSelect");
  const newWorkspaceName = document.getElementById("newWorkspaceName");
  const createWorkspaceBtn = document.getElementById("createWorkspaceBtn");
  const envKeyInput = document.getElementById("envKey");
  const envValueInput = document.getElementById("envValue");
  const setEnvBtn = document.getElementById("setEnvBtn");
  const envVarsDisplay = document.getElementById("envVarsDisplay");

  // Tabs & Request Panels Elements
  const tabsBar = document.getElementById("tabsBar");
  const tabsContent = document.getElementById("tabsContent");
  const addTabBtn = document.getElementById("addTabBtn");

  // Response Section Elements
  const responseBodyTab = document.getElementById("responseBodyTab");
  const responseHeaderTab = document.getElementById("responseHeaderTab");
  const responseBodySection = document.getElementById("responseBodySection");
  const responseHeaderSection = document.getElementById("responseHeaderSection");
  const responseBodyPre = document.getElementById("responseBody");
  const responseHeaderPre = document.getElementById("responseHeader");
  const parseResponseBtn = document.getElementById("parseResponseBtn");
  const rawResponseBtn = document.getElementById("rawResponseBtn");

  // ---------- Workspace & Environment Variables ----------
  async function fetchWorkspaces() {
    try {
      const res = await fetch("/workspaces");
      const workspaces = await res.json();
      workspaceSelect.innerHTML = "";
      workspaces.forEach(ws => {
        const option = document.createElement("option");
        option.value = ws;
        option.textContent = ws;
        workspaceSelect.appendChild(option);
      });
      workspaceSelect.value = currentWorkspace;
    } catch (err) {
      console.error("Error fetching workspaces:", err);
    }
  }

  createWorkspaceBtn.addEventListener("click", async () => {
    const name = newWorkspaceName.value.trim();
    if (!name) return;
    try {
      await fetch("/workspaces", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name })
      });
      newWorkspaceName.value = "";
      currentWorkspace = name;
      await fetchWorkspaces();
      fetchEnvVars();
    } catch (err) {
      console.error("Error creating workspace:", err);
    }
  });

  workspaceSelect.addEventListener("change", () => {
    currentWorkspace = workspaceSelect.value;
    fetchEnvVars();
  });

  async function fetchEnvVars() {
    try {
      const res = await fetch(`/env?workspace=${currentWorkspace}`);
      const envVars = await res.json();
      displayEnvVars(envVars);
    } catch (err) {
      console.error("Error fetching env vars:", err);
    }
  }

  function displayEnvVars(envVars) {
    envVarsDisplay.innerHTML = "";
    for (const key in envVars) {
      const span = document.createElement("span");
      span.className = "bg-gray-200 px-2 py-1 rounded";
      span.textContent = `${key}: ${envVars[key]}`;
      envVarsDisplay.appendChild(span);
    }
  }

  setEnvBtn.addEventListener("click", async () => {
    const key = envKeyInput.value.trim();
    const value = envValueInput.value.trim();
    if (!key) return;
    try {
      await fetch(`/env?workspace=${currentWorkspace}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ [key]: value })
      });
      envKeyInput.value = "";
      envValueInput.value = "";
      fetchEnvVars();
    } catch (err) {
      console.error("Error setting env var:", err);
    }
  });

  // ---------- Tabs & Request Panels ----------
  function createTab() {
    tabCounter++;
    const tabId = "tab-" + tabCounter;
    activeTabId = tabId;

    // Create tab header.
    const tabBtn = document.createElement("button");
    tabBtn.className = "px-4 py-2 border-r hover:bg-gray-100 flex items-center";
    tabBtn.dataset.tabId = tabId;
    tabBtn.innerHTML = `Request ${tabCounter} <span class="ml-2 text-red-500 cursor-pointer closeTabBtn">✕</span>`;
    tabBtn.addEventListener("click", (e) => {
      if (e.target.classList.contains("closeTabBtn")) {
        removeTab(tabId);
      } else {
        switchTab(tabId);
      }
    });
    tabsBar.insertBefore(tabBtn, addTabBtn);

    // Create tab content (request panel).
    const tabPanel = document.createElement("div");
    tabPanel.id = tabId;
    tabPanel.className = "request-panel p-4";
    tabPanel.innerHTML = getRequestPanelHTML(tabId);
    tabsContent.appendChild(tabPanel);

    attachDynamicListeners(tabPanel);
    switchTab(tabId);
  }

  function removeTab(tabId) {
    const tabBtn = tabsBar.querySelector(`button[data-tab-id="${tabId}"]`);
    const tabPanel = document.getElementById(tabId);
    if (tabBtn) tabBtn.remove();
    if (tabPanel) tabPanel.remove();
    if (activeTabId === tabId && tabsBar.querySelector("button[data-tab-id]")) {
      const nextTab = tabsBar.querySelector("button[data-tab-id]");
      switchTab(nextTab.dataset.tabId);
    }
  }

  function switchTab(tabId) {
    activeTabId = tabId;
    document.querySelectorAll(".request-panel").forEach(panel => panel.classList.add("hidden"));
    tabsBar.querySelectorAll("button[data-tab-id]").forEach(btn => btn.classList.remove("bg-gray-200"));
    const activePanel = document.getElementById(tabId);
    if (activePanel) activePanel.classList.remove("hidden");
    const activeBtn = tabsBar.querySelector(`button[data-tab-id="${tabId}"]`);
    if (activeBtn) activeBtn.classList.add("bg-gray-200");
  }

  addTabBtn.addEventListener("click", createTab);
  createTab();

  // Returns compact HTML for a request panel.
  function getRequestPanelHTML(tabId) {
    return `
      <!-- Top Bar: URL and HTTP Method -->
      <div class="flex items-center space-x-2 mb-4">
        <input type="url" class="url flex-grow border rounded p-2 compact-input" placeholder="https://api.example.com/data" required>
        <select class="method border rounded p-1 compact-input">
          <option value="GET">GET</option>
          <option value="POST">POST</option>
          <option value="PUT">PUT</option>
          <option value="DELETE">DELETE</option>
          <option value="PATCH">PATCH</option>
        </select>
      </div>
      <!-- Sub-Tabs: Request and Headers -->
      <div class="sub-tabs border-b mb-4">
        <button class="subTabBtn active-subTab px-3 py-1 compact-input" data-target="req-${tabId}">Request</button>
        <button class="subTabBtn px-3 py-1 compact-input" data-target="hdr-${tabId}">Headers</button>
      </div>
      <!-- Request Panel -->
      <div id="req-${tabId}" class="subTabContent">
        <!-- Body Type Selector -->
        <div class="mb-4">
          <label class="mr-2">Body Type:</label>
          <select class="bodyTypeSelector border rounded p-1 compact-input">
            <option value="raw">Raw</option>
            <option value="keyvalue">Key-Value</option>
            <option value="file">File Upload</option>
          </select>
        </div>
        <!-- Content-Type (dropdown) -->
        <div class="mb-4">
          <label class="mr-2">Content-Type:</label>
          <select class="bodyContentType border rounded p-1 compact-input">
            <option value="application/json">application/json</option>
            <option value="text/plain">text/plain</option>
            <option value="application/x-www-form-urlencoded">application/x-www-form-urlencoded</option>
            <option value="multipart/form-data">multipart/form-data</option>
          </select>
        </div>
        <!-- Raw Body -->
        <div class="bodyRaw mb-4">
          <textarea class="rawBody w-full border rounded p-2 compact-input" placeholder='{"key":"value"}'></textarea>
        </div>
        <!-- Key-Value Body -->
        <div class="bodyKeyValue hidden mb-4">
          <div class="bodyKVPContainer"></div>
          <button type="button" class="addBodyKVPBtn bg-gray-300 px-2 py-1 mt-2 text-sm">+ Field</button>
        </div>
        <!-- File Upload -->
        <div class="bodyFile hidden mb-4">
          <input type="file" class="fileInput border rounded p-2 w-full compact-input" />
        </div>
      </div>
      <!-- Headers Panel -->
      <div id="hdr-${tabId}" class="subTabContent hidden">
        <!-- Header Input Type Selector -->
        <div class="mb-4">
          <label class="mr-2">Header Type:</label>
          <select class="headerInputType border rounded p-1 compact-input">
            <option value="json">JSON</option>
            <option value="keyvalue">Key-Value</option>
          </select>
        </div>
        <!-- JSON Headers -->
        <div class="headersJson mb-4">
          <textarea class="headers w-full border rounded p-2 compact-input" placeholder='{"Authorization": "Bearer token"}'></textarea>
        </div>
        <!-- Key-Value Headers -->
        <div class="headersKVP hidden mb-4">
          <div class="headersKVPContainer"></div>
          <button type="button" class="addHeaderBtn bg-gray-300 px-2 py-1 mt-2 text-sm">+ Header</button>
        </div>
      </div>
      <!-- Send Request Button -->
      <div>
        <button class="sendRequestBtn bg-blue-500 text-white px-4 py-2 rounded compact-input">Send Request</button>
      </div>
    `;
  }

  // ---------- Dynamic Key-Value Pair Functions ----------
  function addKVRow(container) {
    const row = document.createElement("div");
    row.className = "kvRow flex items-center space-x-2 mt-2";
    const keyInput = document.createElement("input");
    keyInput.type = "text";
    keyInput.placeholder = "Key";
    keyInput.className = "kvKey border p-1 compact-input";
    const valueInput = document.createElement("input");
    valueInput.type = "text";
    valueInput.placeholder = "Value";
    valueInput.className = "kvValue border p-1 compact-input";
    const removeBtn = document.createElement("button");
    removeBtn.type = "button";
    removeBtn.textContent = "✕";
    removeBtn.className = "removeKvBtn text-red-500 compact-input";
    removeBtn.addEventListener("click", () => row.remove());
    row.append(keyInput, valueInput, removeBtn);
    container.appendChild(row);
  }

  function attachDynamicListeners(panel) {
    // Sub-tabs switching
    const subTabBtns = panel.querySelectorAll(".subTabBtn");
    subTabBtns.forEach(btn => {
      btn.addEventListener("click", () => {
        const target = btn.dataset.target;
        panel.querySelectorAll(".subTabBtn").forEach(b => b.classList.remove("active-subTab", "bg-gray-200"));
        btn.classList.add("active-subTab", "bg-gray-200");
        panel.querySelectorAll(".subTabContent").forEach(content => content.classList.add("hidden"));
        panel.querySelector("#" + target).classList.remove("hidden");
      });
    });

    // Toggle between body input types
    const bodyTypeSelector = panel.querySelector(".bodyTypeSelector");
    const bodyRaw = panel.querySelector(".bodyRaw");
    const bodyKeyValue = panel.querySelector(".bodyKeyValue");
    const bodyFile = panel.querySelector(".bodyFile");

    bodyTypeSelector.addEventListener("change", () => {
      const type = bodyTypeSelector.value;
      bodyRaw.classList.toggle("hidden", type !== "raw");
      bodyKeyValue.classList.toggle("hidden", type !== "keyvalue");
      bodyFile.classList.toggle("hidden", type !== "file");
    });

    // Toggle between header input types
    const headerInputType = panel.querySelector(".headerInputType");
    const headersJson = panel.querySelector(".headersJson");
    const headersKVP = panel.querySelector(".headersKVP");
    headerInputType.addEventListener("change", () => {
      const type = headerInputType.value;
      headersJson.classList.toggle("hidden", type !== "json");
      headersKVP.classList.toggle("hidden", type !== "keyvalue");
    });

    // Add key-value rows for body and headers.
    const addBodyKVPBtn = panel.querySelector(".addBodyKVPBtn");
    const bodyKVPContainer = panel.querySelector(".bodyKVPContainer");
    addBodyKVPBtn.addEventListener("click", () => addKVRow(bodyKVPContainer));

    const addHeaderBtn = panel.querySelector(".addHeaderBtn");
    const headersKVPContainer = panel.querySelector(".headersKVPContainer");
    addHeaderBtn.addEventListener("click", () => addKVRow(headersKVPContainer));
  }

  // ---------- Send Request Function ----------
  async function sendRequest(panel) {
    const method = panel.querySelector(".method").value;
    const url = panel.querySelector(".url").value;
    let headers = {};

    // Determine header input type.
    const headerType = panel.querySelector(".headerInputType").value;
    if (headerType === "json") {
      try {
        const headersText = panel.querySelector(".headers").value;
        if (headersText.trim()) {
          headers = JSON.parse(headersText);
        }
      } catch (err) {
        return { error: "Invalid JSON in headers." };
      }
    } else {
      const headerRows = panel.querySelectorAll(".headersKVPContainer .kvRow");
      headerRows.forEach(row => {
        const key = row.querySelector(".kvKey").value;
        const value = row.querySelector(".kvValue").value;
        if (key) headers[key] = value;
      });
    }

    // Build payload based on the chosen body type.
    const bodyType = panel.querySelector(".bodyTypeSelector").value;
    let payload = {
      method,
      url,
      headers,
      bodyInputType: bodyType,
    };

    if (bodyType === "raw") {
      payload.rawBody = panel.querySelector(".rawBody").value;
      payload.bodyContentType = panel.querySelector(".bodyContentType").value;
    } else if (bodyType === "keyvalue") {
      const kvRows = panel.querySelectorAll(".bodyKVPContainer .kvRow");
      const bodyKVP = [];
      kvRows.forEach(row => {
        const key = row.querySelector(".kvKey").value;
        const value = row.querySelector(".kvValue").value;
        if (key) bodyKVP.push({ key, value });
      });
      payload.bodyKVP = bodyKVP;
      payload.bodyContentType = panel.querySelector(".bodyContentType").value;
    } else if (bodyType === "file") {
      const fileInput = panel.querySelector(".fileInput");
      const file = fileInput.files[0];
      if (!file) return { error: "No file selected." };
      const formData = new FormData();
      formData.append("method", method);
      formData.append("url", url);
      formData.append("headers", JSON.stringify(headers));
      formData.append("bodyInputType", "file");
      formData.append("file", file);
      payload = formData;
    }

    let options = {};
    if (bodyType === "file") {
      options = { method: "POST", body: payload };
    } else {
      options = {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload)
      };
    }
    try {
      const res = await fetch(`/sendRequest?workspace=${currentWorkspace}`, options);
      if (!res.ok) return { error: `Error: ${res.statusText}` };
      const data = await res.json();
      if (data.envVars) displayEnvVars(data.envVars);
      return data;
    } catch (error) {
      return { error: "Request failed: " + error.message };
    }
  }

  // Delegate send request button click.
  tabsContent.addEventListener("click", async (e) => {
    if (e.target.classList.contains("sendRequestBtn")) {
      const panel = e.target.closest(".request-panel");
      const response = await sendRequest(panel);
      displayResponse(response);
    }
  });

  // ---------- Display Response Function ----------
  function displayResponse(response) {
    latestRawResponse = response.body || "";
    responseBodyPre.textContent = latestRawResponse;
    const formattedHeaders = Object.entries(response.headers || {}).map(([key, value]) => {
      const formattedValue = Array.isArray(value) ? value.join(",") : value;
      return `${key}: ${formattedValue}`;
    }).join("\n");
    responseHeaderPre.textContent = formattedHeaders;
  }

  // Response Tabs Toggle
  responseBodyTab.addEventListener("click", () => {
    responseBodyTab.classList.add("bg-gray-200");
    responseHeaderTab.classList.remove("bg-gray-200");
    responseBodySection.classList.remove("hidden");
    responseHeaderSection.classList.add("hidden");
  });

  responseHeaderTab.addEventListener("click", () => {
    responseHeaderTab.classList.add("bg-gray-200");
    responseBodyTab.classList.remove("bg-gray-200");
    responseHeaderSection.classList.remove("hidden");
    responseBodySection.classList.add("hidden");
  });

  parseResponseBtn.addEventListener("click", () => {
    try {
      const parsed = JSON.parse(latestRawResponse);
      responseBodyPre.textContent = JSON.stringify(parsed, null, 2);
    } catch (e) {
      responseBodyPre.innerHTML = latestRawResponse;
    }
  });

  rawResponseBtn.addEventListener("click", () => {
    responseBodyPre.textContent = latestRawResponse;
  });

  // ---------- Initialize ----------
  fetchWorkspaces();
  fetchEnvVars();
});
