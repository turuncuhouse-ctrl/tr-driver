package net.neciparmagan.trdriver.data

import java.io.IOException

class HttpStatusIOException(
    val code: Int,
    message: String,
    val retryAfterSec: Int = 0,
) : IOException(message)
