import './i18n'
import React, { Suspense } from 'react'
import {createRoot} from 'react-dom/client'
import '@shared/styles.css'
import './App.css'
import App from './App'

const container = document.getElementById('root')

const root = createRoot(container!)

root.render(
    <React.StrictMode>
        <Suspense fallback={null}>
            <App/>
        </Suspense>
    </React.StrictMode>
)
