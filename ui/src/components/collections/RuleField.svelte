<script context="module">
    let cachedRuleComponent;
    let cachedCodeEditorComponent;
</script>

<script>
    import { tick } from "svelte";
    import { scale, slide } from "svelte/transition";
    import Field from "@/components/base/Field.svelte";
    import tooltip from "@/actions/tooltip";
    import { collections } from "@/stores/collections";
    import ApiClient from "@/utils/ApiClient";
    import CommonHelper from "@/utils/CommonHelper";

    export let collection = null;
    export let rule = null;
    export let label = "Rule";
    export let formKey = "rule";
    export let required = false;
    export let disabled = false;
    export let superuserToggle = true;
    export let placeholder = "Leave empty to grant everyone access...";

    const uniqueId = "rule_" + CommonHelper.randomString(5);

    let editorRef = null;
    let tempValue = null;
    let ruleInputComponent = cachedRuleComponent;
    let codeEditorComponent = cachedCodeEditorComponent;
    let isRuleComponentLoading = false;

    let selectedAuthCollectionId = "";
    let sqlResult = "";
    let worstCaseSqlResult = "";
    let explainResult = null;
    let cheapBranchesResult = null;
    let sqlLoading = false;
    let debounceTimer;

    $: authCollections = $collections.filter((c) => c.type === "auth");

    $: isSuperuserOnly = superuserToggle && rule === null;

    $: isDisabled = disabled || collection.system;

    $: if (rule && collection?.id && selectedAuthCollectionId !== undefined) {
        debouncedFetchSQL();
    } else {
        sqlResult = "";
        worstCaseSqlResult = "";
        explainResult = null;
        cheapBranchesResult = null;
    }

    loadEditorComponent();

    async function loadEditorComponent() {
        if (ruleInputComponent || isRuleComponentLoading) {
            return; // already loaded or in the process
        }

        isRuleComponentLoading = true;

        const [filterModule, codeEditorModule] = await Promise.all([
            import("@/components/base/FilterAutocompleteInput.svelte"),
            import("@/components/base/CodeEditor.svelte"),
        ]);

        ruleInputComponent = filterModule.default;
        cachedRuleComponent = ruleInputComponent;

        codeEditorComponent = codeEditorModule.default;
        cachedCodeEditorComponent = codeEditorComponent;

        isRuleComponentLoading = false;
    }

    async function fetchSQL() {
        if (!rule || !collection?.id) {
            sqlResult = "";
            worstCaseSqlResult = "";
            explainResult = null;
            cheapBranchesResult = null;
            return;
        }
        sqlLoading = true;
        try {
            const result = await ApiClient.send(
                `/api/collections/${encodeURIComponent(collection.id)}/render-rule`,
                {
                    method: "POST",
                    body: { rule, explain: true, authCollectionId: selectedAuthCollectionId || undefined },
                    requestKey: uniqueId,
                },
            );
            sqlResult = result.sql || "";
            worstCaseSqlResult = result.worstCaseSql || "";
            explainResult = result.explain || null;
            cheapBranchesResult = result.cheapBranches || null;
        } catch (err) {
            if (!err.isAbort) {
                sqlResult = "Error: " + (err?.data?.message || err.message || "Unknown error");
                worstCaseSqlResult = "";
                explainResult = null;
                cheapBranchesResult = null;
            }
        }
        sqlLoading = false;
    }

    function debouncedFetchSQL() {
        clearTimeout(debounceTimer);
        debounceTimer = setTimeout(fetchSQL, 300);
    }

    function formatExplain(rows) {
        if (!rows?.length) return "No query plan available.";
        return rows.map((r) => r.detail).join("\n");
    }

    async function unlock() {
        rule = tempValue || "";
        await tick();
        editorRef?.focus();
    }

    function lock() {
        tempValue = rule;
        rule = null;
    }
</script>

{#if isRuleComponentLoading}
    <div class="txt-center">
        <span class="loader" />
    </div>
{:else}
    <Field
        class="form-field rule-field {required ? 'requied' : ''} {isSuperuserOnly ? 'disabled' : ''}"
        name={formKey}
        let:uniqueId
    >
        <div
            class="input-wrapper"
            use:tooltip={collection.system
                ? { text: "System collection rule cannot be changed.", position: "top" }
                : undefined}
        >
            <label for={uniqueId}>
                <slot name="beforeLabel" {isSuperuserOnly} />

                <span class="txt" class:txt-hint={isSuperuserOnly}>
                    {label}
                    {isSuperuserOnly ? "- Superusers only" : ""}
                </span>

                <slot name="afterLabel" {isSuperuserOnly} />

                {#if superuserToggle && !isSuperuserOnly}
                    <div class="rule-actions">
                        {#if authCollections.length && rule}
                            <select
                                class="view-as-select txt-xs"
                                bind:value={selectedAuthCollectionId}
                                disabled={isDisabled}
                            >
                                <option value="">View as: Guest</option>
                                {#each authCollections as authCol}
                                    <option value={authCol.id}>{authCol.name}</option>
                                {/each}
                            </select>
                        {/if}
                        <button
                            type="button"
                            class="btn btn-sm btn-transparent btn-hint lock-toggle"
                            aria-hidden={isDisabled}
                            disabled={isDisabled}
                            on:click={lock}
                        >
                            <i class="ri-lock-line" aria-hidden="true" />
                            <span class="txt">Set Superusers only</span>
                        </button>
                    </div>
                {/if}
            </label>

            <svelte:component
                this={ruleInputComponent}
                id={uniqueId}
                bind:this={editorRef}
                bind:value={rule}
                baseCollection={collection}
                disabled={isDisabled || isSuperuserOnly}
                placeholder={!isSuperuserOnly ? placeholder : ""}
            />

            {#if superuserToggle && isSuperuserOnly}
                <button
                    type="button"
                    class="unlock-overlay"
                    disabled={isDisabled}
                    aria-hidden={isDisabled}
                    transition:scale={{ duration: 150, start: 0.98 }}
                    on:click={unlock}
                >
                    {#if !isDisabled}
                        <small class="txt">Unlock and set custom rule</small>
                    {/if}
                    <div class="icon" aria-hidden="true">
                        <i class="ri-lock-unlock-line" />
                    </div>
                </button>
            {/if}
        </div>

        {#if sqlLoading}
            <div class="block txt-center p-10">
                <span class="loader loader-sm active" />
            </div>
        {:else if sqlResult}
            <div class="sql-preview" transition:slide={{ duration: 150 }}>
                <label>
                    <span class="txt txt-hint">SQL</span>
                </label>
                <svelte:component
                    this={codeEditorComponent}
                    value={sqlResult}
                    language="sql-select"
                    disabled={true}
                    maxHeight="200"
                />
                {#if cheapBranchesResult?.length}
                    <div class="cheap-branches m-t-5">
                        <label><span class="txt txt-hint">Short-circuits when</span></label>
                        {#each cheapBranchesResult as branch}
                            <code class="txt-sm">{branch.expression}</code>
                        {/each}
                    </div>
                {/if}
                {#if worstCaseSqlResult && worstCaseSqlResult !== sqlResult}
                    <label class="m-t-10">
                        <span class="txt txt-hint">Worst-case SQL</span>
                    </label>
                    <svelte:component
                        this={codeEditorComponent}
                        value={worstCaseSqlResult}
                        language="sql-select"
                        disabled={true}
                        maxHeight="200"
                    />
                {/if}
                {#if explainResult}
                    <label class="m-t-10">
                        <span class="txt txt-hint">Query plan</span>
                    </label>
                    <pre class="txt-sm explain-output">{formatExplain(explainResult)}</pre>
                {/if}
            </div>
        {/if}

        <div class="help-block">
            <slot {isSuperuserOnly} />
        </div>
    </Field>
{/if}

<style lang="scss">
    .sql-preview {
        margin-top: 5px;
        label {
            display: block;
            margin-bottom: 3px;
        }
    }
    .explain-output {
        white-space: pre-wrap;
        word-break: break-all;
        margin: 0;
        padding: 8px;
        background: var(--baseAlt1Color);
        border-radius: var(--baseRadius);
    }
    .rule-actions {
        position: absolute;
        right: 0px;
        top: 0px;
        display: flex;
        align-items: stretch;
        gap: 0;
    }
    .view-as-select {
        padding: 6px 8px;
        border: 0;
        border-bottom-left-radius: 0;
        border-top-right-radius: 0;
        border-bottom-right-radius: 0;
        background: rgba(53, 71, 104, 0.09);
        color: var(--txtHintColor);
        cursor: pointer;
        outline: none;
        &:hover,
        &:focus {
            color: var(--txtPrimaryColor);
        }
    }
    .lock-toggle {
        min-width: 135px;
        padding: 10px;
        border-top-left-radius: 0;
        border-bottom-right-radius: 0;
        background: rgba(53, 71, 104, 0.09);
    }
    :global(.rule-field .code-editor .cm-placeholder) {
        font-family: var(--baseFontFamily);
    }
    .input-wrapper {
        position: relative;
    }
    .unlock-overlay {
        --hoverAnimationSpeed: 0.2s;
        position: absolute;
        z-index: 1;
        left: 0;
        top: 0;
        width: 100%;
        height: 100%;
        display: flex;
        padding: 20px;
        gap: 10px;
        align-items: center;
        justify-content: end;
        text-align: center;
        border-radius: var(--baseRadius);
        outline: 0;
        cursor: pointer;
        text-decoration: none;
        color: var(--successColor);
        border: 2px solid var(--baseAlt1Color);
        transition: border-color var(--baseAnimationSpeed);
        i {
            font-size: inherit;
        }
        .icon {
            color: var(--successColor);
            font-size: 1.15rem;
            line-height: 1;
            font-weight: normal;
            transition: transform var(--hoverAnimationSpeed);
        }
        .txt {
            opacity: 0;
            font-size: var(--xsFontSize);
            font-weight: 600;
            line-height: var(--smLineHeight);
            transform: translateX(5px);
            transition:
                transform var(--hoverAnimationSpeed),
                opacity var(--hoverAnimationSpeed);
        }
        &:hover,
        &:focus-visible,
        &:active {
            border-color: var(--baseAlt3Color);
            .icon {
                transform: scale(1.1);
            }
            .txt {
                opacity: 1;
                transform: scale(1);
            }
        }
        &:active {
            transition-duration: var(--activeAnimationSpeed);
            border-color: var(--baseAlt3Color);
        }
        &[disabled] {
            cursor: not-allowed;
        }
    }
</style>
